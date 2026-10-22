package main

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthUser struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type AuthResponse struct {
	User        AuthUser `json:"user"`
	AccessToken string   `json:"access_token"`
	TokenType   string   `json:"token_type"`
	ExpiresIn   int64    `json:"expires_in"`
}

type TokenClaims struct {
	jwt.RegisteredClaims
	UserID int64 `json:"user_id"`
}

type contextKey string

const userIDContextKey contextKey = "authenticated_user_id"

const accessTokenLifetime = time.Hour

func registerHandler(
	pool *pgxpool.Pool,
	jwtSecret string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input RegisterRequest

		if err := decodeJSON(w, r, &input); err != nil {
			writeDecodeError(w, err)
			return
		}

		input.Name = strings.TrimSpace(input.Name)
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))

		if input.Name == "" ||
			utf8.RuneCountInString(input.Name) > 100 {
			writeError(
				w,
				http.StatusBadRequest,
				"name is required and must be at most 100 characters",
			)
			return
		}

		if len(input.Email) > 255 {
			writeError(w, http.StatusBadRequest, "Invalid email address")
			return
		}

		address, err := mail.ParseAddress(input.Email)
		if err != nil || address.Address != input.Email {
			writeError(w, http.StatusBadRequest, "Invalid email address")
			return
		}

		if len(input.Password) < 8 || len(input.Password) > 72 {
			writeError(
				w,
				http.StatusBadRequest,
				"Password must be between 8 and 72 bytes",
			)
			return
		}

		passwordHash, err := bcrypt.GenerateFromPassword(
			[]byte(input.Password),
			bcrypt.DefaultCost,
		)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to process password",
			)
			return
		}

		var user AuthUser

		err = pool.QueryRow(r.Context(), `
			INSERT INTO users (name, email, password_hash)
			VALUES ($1, $2, $3)
			RETURNING id, name, email, created_at
		`,
			input.Name,
			input.Email,
			string(passwordHash),
		).Scan(
			&user.ID,
			&user.Name,
			&user.Email,
			&user.CreatedAt,
		)

		if err != nil {
			if isUniqueViolation(err) {
				writeError(
					w,
					http.StatusConflict,
					"Email is already registered",
				)
				return
			}

			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to register user",
			)
			return
		}

		response, err := createAuthResponse(user, jwtSecret)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to create access token",
			)
			return
		}

		writeJSON(w, http.StatusCreated, response)
	}
}

func loginHandler(
	pool *pgxpool.Pool,
	jwtSecret string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input LoginRequest

		if err := decodeJSON(w, r, &input); err != nil {
			writeDecodeError(w, err)
			return
		}

		email := strings.ToLower(strings.TrimSpace(input.Email))

		if email == "" || input.Password == "" {
			writeError(
				w,
				http.StatusBadRequest,
				"Email and password are required",
			)
			return
		}

		var user AuthUser
		var passwordHash string

		err := pool.QueryRow(r.Context(), `
			SELECT id, name, email, password_hash, created_at
			FROM users
			WHERE email = $1
		`, email).Scan(
			&user.ID,
			&user.Name,
			&user.Email,
			&passwordHash,
			&user.CreatedAt,
		)

		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(
					w,
					http.StatusUnauthorized,
					"Invalid email or password",
				)
				return
			}

			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to authenticate user",
			)
			return
		}

		if err := bcrypt.CompareHashAndPassword(
			[]byte(passwordHash),
			[]byte(input.Password),
		); err != nil {
			writeError(
				w,
				http.StatusUnauthorized,
				"Invalid email or password",
			)
			return
		}

		response, err := createAuthResponse(user, jwtSecret)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to create access token",
			)
			return
		}

		writeJSON(w, http.StatusOK, response)
	}
}

func createAuthResponse(
	user AuthUser,
	jwtSecret string,
) (AuthResponse, error) {
	now := time.Now()
	expiresAt := now.Add(accessTokenLifetime)

	claims := TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		UserID: user.ID,
	}

	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		claims,
	)

	signedToken, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return AuthResponse{}, err
	}

	return AuthResponse{
		User:        user,
		AccessToken: signedToken,
		TokenType:   "Bearer",
		ExpiresIn:   int64(accessTokenLifetime.Seconds()),
	}, nil
}

func authMiddleware(
	jwtSecret string,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))

		if len(parts) != 2 ||
			!strings.EqualFold(parts[0], "Bearer") {
			writeError(
				w,
				http.StatusUnauthorized,
				"Bearer token is required",
			)
			return
		}

		claims := &TokenClaims{}

		token, err := jwt.ParseWithClaims(
			parts[1],
			claims,
			func(token *jwt.Token) (any, error) {
				if token.Method != jwt.SigningMethodHS256 {
					return nil, errors.New("unexpected signing method")
				}

				return []byte(jwtSecret), nil
			},
			jwt.WithValidMethods([]string{"HS256"}),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		)

		if err != nil ||
			token == nil ||
			!token.Valid ||
			claims.UserID <= 0 {
			writeError(
				w,
				http.StatusUnauthorized,
				"Invalid or expired access token",
			)
			return
		}

		ctx := context.WithValue(
			r.Context(),
			userIDContextKey,
			claims.UserID,
		)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func meHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := r.Context().
			Value(userIDContextKey).
			(int64)

		if !ok || userID <= 0 {
			writeError(
				w,
				http.StatusUnauthorized,
				"Authentication required",
			)
			return
		}

		var user AuthUser

		err := pool.QueryRow(r.Context(), `
			SELECT id, name, email, created_at
			FROM users
			WHERE id = $1
		`, userID).Scan(
			&user.ID,
			&user.Name,
			&user.Email,
			&user.CreatedAt,
		)

		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(
					w,
					http.StatusUnauthorized,
					"User no longer exists",
				)
				return
			}

			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to fetch user profile",
			)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"user": user,
		})
	}
}

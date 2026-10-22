package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAuthMiddlewareRejectsInvalidAuthorization(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
	}{
		{
			name:          "missing token",
			authorization: "",
		},
		{
			name:          "missing bearer prefix",
			authorization: "abc.def.ghi",
		},
		{
			name:          "bearer without token",
			authorization: "Bearer",
		},
		{
			name:          "malformed token",
			authorization: "Bearer invalid-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false

			next := http.HandlerFunc(func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})

			handler := authMiddleware("test-secret", next)

			req := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/me",
				nil,
			)
			req.Header.Set("Authorization", tt.authorization)

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf(
					"expected status %d, got %d; body: %s",
					http.StatusUnauthorized,
					recorder.Code,
					recorder.Body.String(),
				)
			}

			if nextCalled {
				t.Fatal("protected handler should not be called")
			}
		})
	}
}

func TestAuthMiddlewareRejectsInvalidJWTClaims(t *testing.T) {
	tests := []struct {
		name   string
		claims TokenClaims
	}{
		{
			name: "expired token",
			claims: TokenClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
				},
				UserID: 1,
			},
		},
		{
			name: "missing expiration",
			claims: TokenClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					IssuedAt: jwt.NewNumericDate(time.Now()),
				},
				UserID: 1,
			},
		},
		{
			name: "invalid user ID",
			claims: TokenClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					IssuedAt:  jwt.NewNumericDate(time.Now()),
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				},
				UserID: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := jwt.NewWithClaims(
				jwt.SigningMethodHS256,
				tt.claims,
			)

			signedToken, err := token.SignedString([]byte("test-secret"))
			if err != nil {
				t.Fatalf("failed to sign test token: %v", err)
			}

			nextCalled := false
			next := http.HandlerFunc(func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})

			handler := authMiddleware("test-secret", next)

			req := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/me",
				nil,
			)
			req.Header.Set("Authorization", "Bearer "+signedToken)

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf(
					"expected status %d, got %d; body: %s",
					http.StatusUnauthorized,
					recorder.Code,
					recorder.Body.String(),
				)
			}

			if nextCalled {
				t.Fatal("protected handler should not be called")
			}
		})
	}
}

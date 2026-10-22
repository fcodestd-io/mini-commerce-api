package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Category struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Product struct {
	ID          int64     `json:"id"`
	CategoryID  *int64    `json:"category_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Price       int64     `json:"price"`
	Stock       int       `json:"stock"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProductInput struct {
	CategoryID  *int64 `json:"category_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       int64  `json:"price"`
	Stock       int    `json:"stock"`
}

type CategoryInput struct {
	Name string `json:"name"`
}

type ProductListResponse struct {
	Data       []Product `json:"data"`
	Page       int       `json:"page"`
	Limit      int       `json:"limit"`
	Total      int64     `json:"total"`
	TotalPages int64     `json:"total_pages"`
}

func categoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			rows, err := pool.Query(r.Context(), `
				SELECT id, name, created_at
				FROM categories
				ORDER BY id
			`)
			if err != nil {
				writeError(w, http.StatusInternalServerError,
					"Failed to fetch categories")
				return
			}
			defer rows.Close()

			categories := make([]Category, 0)
			for rows.Next() {
				var category Category
				if err := rows.Scan(
					&category.ID,
					&category.Name,
					&category.CreatedAt,
				); err != nil {
					writeError(w, http.StatusInternalServerError,
						"Failed to read categories")
					return
				}
				categories = append(categories, category)
			}

			if err := rows.Err(); err != nil {
				writeError(w, http.StatusInternalServerError,
					"Failed to read categories")
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"data": categories,
			})

		case http.MethodPost:
			var input CategoryInput
			if err := decodeJSON(w, r, &input); err != nil {
				writeDecodeError(w, err)
				return
			}

			input.Name = strings.TrimSpace(input.Name)
			if input.Name == "" || len(input.Name) > 100 {
				writeError(w, http.StatusBadRequest,
					"name is required and must be at most 100 characters")
				return
			}

			var category Category
			err := pool.QueryRow(r.Context(), `
				INSERT INTO categories (name)
				VALUES ($1)
				RETURNING id, name, created_at
			`, input.Name).Scan(
				&category.ID,
				&category.Name,
				&category.CreatedAt,
			)
			if err != nil {
				if isUniqueViolation(err) {
					writeError(w, http.StatusConflict,
						"Category name already exists")
					return
				}
				writeError(w, http.StatusInternalServerError,
					"Failed to create category")
				return
			}

			writeJSON(w, http.StatusCreated, category)

		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed,
				"Method not allowed")
		}
	}
}

func productsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listProducts(w, r, pool)
		case http.MethodPost:
			createProduct(w, r, pool)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed,
				"Method not allowed")
		}
	}
}

func productByIDHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "Invalid product ID")
			return
		}

		switch r.Method {
		case http.MethodGet:
			getProduct(w, r, pool, id)
		case http.MethodPut:
			updateProduct(w, r, pool, id)
		case http.MethodDelete:
			deleteProduct(w, r, pool, id)
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			writeError(w, http.StatusMethodNotAllowed,
				"Method not allowed")
		}
	}
}

func listProducts(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
) {
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 10)

	if limit > 100 {
		limit = 100
	}

	var total int64
	if err := pool.QueryRow(
		r.Context(),
		`SELECT COUNT(*) FROM products`,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError,
			"Failed to count products")
		return
	}

	rows, err := pool.Query(r.Context(), `
		SELECT id, category_id, name, description, price, stock,
		       created_at, updated_at
		FROM products
		ORDER BY id
		LIMIT $1 OFFSET $2
	`, limit, (page-1)*limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			"Failed to fetch products")
		return
	}
	defer rows.Close()

	products := make([]Product, 0)
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError,
				"Failed to read products")
			return
		}
		products = append(products, product)
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError,
			"Failed to read products")
		return
	}

	totalPages := int64(0)
	if total > 0 {
		totalPages = (total + int64(limit) - 1) / int64(limit)
	}

	writeJSON(w, http.StatusOK, ProductListResponse{
		Data:       products,
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: totalPages,
	})
}

func createProduct(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
) {
	var input ProductInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}

	if message := validateProduct(input); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}

	if input.CategoryID != nil {
		if err := ensureCategoryExists(r.Context(), pool, *input.CategoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "Category not found")
				return
			}
			writeError(w, http.StatusInternalServerError,
				"Failed to validate category")
			return
		}
	}

	product, err := insertProduct(r.Context(), pool, input)
	if err != nil {
		if isForeignKeyViolation(err) {
			writeError(w, http.StatusBadRequest, "Category not found")
			return
		}
		writeError(w, http.StatusInternalServerError,
			"Failed to create product")
		return
	}

	writeJSON(w, http.StatusCreated, product)
}

func getProduct(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	id int64,
) {
	product, err := fetchProduct(r.Context(), pool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Product not found")
			return
		}
		writeError(w, http.StatusInternalServerError,
			"Failed to fetch product")
		return
	}

	writeJSON(w, http.StatusOK, product)
}

func updateProduct(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	id int64,
) {
	var input ProductInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}

	if message := validateProduct(input); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}

	if input.CategoryID != nil {
		if err := ensureCategoryExists(r.Context(), pool, *input.CategoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "Category not found")
				return
			}
			writeError(w, http.StatusInternalServerError,
				"Failed to validate category")
			return
		}
	}

	product, err := updateProductRecord(r.Context(), pool, id, input)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Product not found")
			return
		}
		if isForeignKeyViolation(err) {
			writeError(w, http.StatusBadRequest, "Category not found")
			return
		}
		writeError(w, http.StatusInternalServerError,
			"Failed to update product")
		return
	}

	writeJSON(w, http.StatusOK, product)
}

func deleteProduct(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	id int64,
) {
	tag, err := pool.Exec(r.Context(),
		`DELETE FROM products WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			"Failed to delete product")
		return
	}

	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "Product not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func insertProduct(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ProductInput,
) (Product, error) {
	return scanProduct(pool.QueryRow(ctx, `
		INSERT INTO products (category_id, name, description, price, stock)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, category_id, name, description, price, stock,
		          created_at, updated_at
	`, input.CategoryID, strings.TrimSpace(input.Name),
		input.Description, input.Price, input.Stock))
}

func fetchProduct(
	ctx context.Context,
	pool *pgxpool.Pool,
	id int64,
) (Product, error) {
	return scanProduct(pool.QueryRow(ctx, `
		SELECT id, category_id, name, description, price, stock,
		       created_at, updated_at
		FROM products WHERE id = $1
	`, id))
}

func updateProductRecord(
	ctx context.Context,
	pool *pgxpool.Pool,
	id int64,
	input ProductInput,
) (Product, error) {
	return scanProduct(pool.QueryRow(ctx, `
		UPDATE products
		SET category_id = $1,
		    name = $2,
		    description = $3,
		    price = $4,
		    stock = $5,
		    updated_at = NOW()
		WHERE id = $6
		RETURNING id, category_id, name, description, price, stock,
		          created_at, updated_at
	`, input.CategoryID, strings.TrimSpace(input.Name),
		input.Description, input.Price, input.Stock, id))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProduct(row rowScanner) (Product, error) {
	var product Product
	err := row.Scan(
		&product.ID,
		&product.CategoryID,
		&product.Name,
		&product.Description,
		&product.Price,
		&product.Stock,
		&product.CreatedAt,
		&product.UpdatedAt,
	)
	return product, err
}

func validateProduct(input ProductInput) string {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 150 {
		return "name is required and must be at most 150 characters"
	}
	if input.Price < 0 {
		return "price must not be negative"
	}
	if input.Stock < 0 {
		return "stock must not be negative"
	}
	if input.CategoryID != nil && *input.CategoryID <= 0 {
		return "category_id must be a positive integer"
	}
	if len(input.Description) > 5000 {
		return "description must not exceed 5000 characters"
	}
	return ""
}

func ensureCategoryExists(
	ctx context.Context,
	pool *pgxpool.Pool,
	id int64,
) error {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM categories WHERE id = $1)`,
		id,
	).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	return nil
}

func parsePositiveInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}


func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	contentType := strings.TrimSpace(strings.Split(
		r.Header.Get("Content-Type"), ";")[0],
	)

	if contentType != "application/json" {
		return &contentTypeError{
			message: "Content-Type must be application/json",
		}
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(destination); err != nil {
		return err
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request must contain only one JSON object")
		}

		return err
	}

	return nil
}

type contentTypeError struct {
	message string
}

func (e *contentTypeError) Error() string {
	return e.message
}


func isUniqueViolation(err error) bool {
	var pgErr interface {
		SQLState() string
	}

	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr interface {
		SQLState() string
	}

	return errors.As(err, &pgErr) && pgErr.SQLState() == "23503"
}

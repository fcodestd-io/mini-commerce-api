package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func setupCheckoutIntegrationTest(t *testing.T) (
	*pgxpool.Pool,
	int64,
	int64,
	int64,
) {
	t.Helper()

	if err := godotenv.Load(); err != nil {
		t.Fatalf("load .env: %v", err)
	}

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is not set")
	}

	pool, err := OpenDB(databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	t.Cleanup(pool.Close)

	ctx := context.Background()

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var userID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id
	`,
		"Checkout Test User",
		"checkout-test-"+suffix+"@example.com",
		"test-password-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}

	var categoryID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO categories (name)
		VALUES ($1)
		RETURNING id
	`, "Checkout Test Category "+suffix).Scan(&categoryID)
	if err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		t.Fatalf("create test category: %v", err)
	}

	var productID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO products (
			category_id,
			name,
			price,
			stock
		)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`,
		categoryID,
		"Checkout Test Product "+suffix,
		10000,
		10,
	).Scan(&productID)
	if err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, categoryID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		t.Fatalf("create test product: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()

		// Hapus hanya data yang dibuat untuk user test ini.
		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM order_items
			WHERE order_id IN (
				SELECT id FROM orders WHERE user_id = $1
			)
		`, userID)

		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM orders WHERE user_id = $1
		`, userID)

		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM cart_items
			WHERE cart_id IN (
				SELECT id FROM carts WHERE user_id = $1
			)
		`, userID)

		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM carts WHERE user_id = $1
		`, userID)

		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM products WHERE id = $1
		`, productID)

		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM categories WHERE id = $1
		`, categoryID)

		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM users WHERE id = $1
		`, userID)
	})

	return pool, userID, productID, categoryID
}

func addCheckoutTestCartItem(
	t *testing.T,
	pool *pgxpool.Pool,
	userID int64,
	productID int64,
	quantity int,
) {
	t.Helper()

	ctx := context.Background()

	var cartID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO carts (user_id)
		VALUES ($1)
		ON CONFLICT (user_id)
		DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING id
	`, userID).Scan(&cartID)
	if err != nil {
		t.Fatalf("create test cart: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO cart_items (cart_id, product_id, quantity)
		VALUES ($1, $2, $3)
	`, cartID, productID, quantity)
	if err != nil {
		t.Fatalf("create test cart item: %v", err)
	}
}

func newCheckoutTestRequest(
	userID int64,
) *http.Request {
	req := httptest.NewRequest(
		http.MethodPost,
		"/checkout",
		bytes.NewBufferString("{}"),
	)

	ctx := context.WithValue(
		req.Context(),
		userIDContextKey,
		userID,
	)

	return req.WithContext(ctx)
}

func TestCheckoutIntegrationSuccess(t *testing.T) {
	pool, userID, productID, _ :=
		setupCheckoutIntegrationTest(t)

	addCheckoutTestCartItem(t, pool, userID, productID, 2)

	handler := checkoutHandler(pool)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		newCheckoutTestRequest(userID),
	)

	if recorder.Code != http.StatusCreated {
		t.Fatalf(
			"expected status %d, got %d; body: %s",
			http.StatusCreated,
			recorder.Code,
			recorder.Body.String(),
		)
	}

	var response struct {
		Message string `json:"message"`
		Order   struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
			Total  int64  `json:"total"`
		} `json:"order"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode checkout response: %v", err)
	}

	if response.Order.Total != 20000 {
		t.Errorf(
			"expected total 20000, got %d",
			response.Order.Total,
		)
	}

	if response.Order.Status != "pending" {
		t.Errorf(
			"expected status pending, got %q",
			response.Order.Status,
		)
	}

	ctx := context.Background()

	var orderCount int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM orders
		WHERE id = $1 AND user_id = $2
	`, response.Order.ID, userID).Scan(&orderCount)
	if err != nil {
		t.Fatalf("count created order: %v", err)
	}

	if orderCount != 1 {
		t.Fatalf("expected 1 order, got %d", orderCount)
	}

	var itemCount int
	var itemQuantity int
	var unitPrice int64
	var lineTotal int64

	err = pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM order_items
		WHERE order_id = $1
	`, response.Order.ID).Scan(&itemCount)
	if err != nil {
		t.Fatalf("count order items: %v", err)
	}

	if itemCount != 1 {
		t.Fatalf("expected 1 order item, got %d", itemCount)
	}

	err = pool.QueryRow(ctx, `
		SELECT quantity, unit_price, line_total
		FROM order_items
		WHERE order_id = $1 AND product_id = $2
	`, response.Order.ID, productID).Scan(
		&itemQuantity,
		&unitPrice,
		&lineTotal,
	)
	if err != nil {
		t.Fatalf("read order item: %v", err)
	}

	if itemQuantity != 2 {
		t.Errorf("expected item quantity 2, got %d", itemQuantity)
	}

	if unitPrice != 10000 {
		t.Errorf("expected unit price 10000, got %d", unitPrice)
	}

	if lineTotal != 20000 {
		t.Errorf("expected line total 20000, got %d", lineTotal)
	}

	var stock int
	err = pool.QueryRow(ctx, `
		SELECT stock FROM products WHERE id = $1
	`, productID).Scan(&stock)
	if err != nil {
		t.Fatalf("read product stock: %v", err)
	}

	if stock != 8 {
		t.Errorf("expected stock 8, got %d", stock)
	}

	var remainingCartItems int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM cart_items ci
		JOIN carts c ON c.id = ci.cart_id
		WHERE c.user_id = $1
	`, userID).Scan(&remainingCartItems)
	if err != nil {
		t.Fatalf("count remaining cart items: %v", err)
	}

	if remainingCartItems != 0 {
		t.Errorf(
			"expected empty cart, got %d items",
			remainingCartItems,
		)
	}
}

func TestCheckoutIntegrationInsufficientStock(t *testing.T) {
	pool, userID, productID, _ :=
		setupCheckoutIntegrationTest(t)

	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		UPDATE products SET stock = 1 WHERE id = $1
	`, productID)
	if err != nil {
		t.Fatalf("set test stock: %v", err)
	}

	addCheckoutTestCartItem(t, pool, userID, productID, 2)

	var ordersBefore int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM orders WHERE user_id = $1
	`, userID).Scan(&ordersBefore)
	if err != nil {
		t.Fatalf("count orders before checkout: %v", err)
	}

	handler := checkoutHandler(pool)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		newCheckoutTestRequest(userID),
	)

	if recorder.Code != http.StatusConflict {
		t.Fatalf(
			"expected status %d, got %d; body: %s",
			http.StatusConflict,
			recorder.Code,
			recorder.Body.String(),
		)
	}

	var stock int
	err = pool.QueryRow(ctx, `
		SELECT stock FROM products WHERE id = $1
	`, productID).Scan(&stock)
	if err != nil {
		t.Fatalf("read stock after failed checkout: %v", err)
	}

	if stock != 1 {
		t.Errorf(
			"expected stock to remain 1, got %d",
			stock,
		)
	}

	var cartQuantity int
	err = pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(ci.quantity), 0)
		FROM cart_items ci
		JOIN carts c ON c.id = ci.cart_id
		WHERE c.user_id = $1
	`, userID).Scan(&cartQuantity)
	if err != nil {
		t.Fatalf("read cart after failed checkout: %v", err)
	}

	if cartQuantity != 2 {
		t.Errorf(
			"expected cart quantity to remain 2, got %d",
			cartQuantity,
		)
	}

	var ordersAfter int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM orders WHERE user_id = $1
	`, userID).Scan(&ordersAfter)
	if err != nil {
		t.Fatalf("count orders after checkout: %v", err)
	}

	if ordersAfter != ordersBefore {
		t.Errorf(
			"expected order count to remain %d, got %d",
			ordersBefore,
			ordersAfter,
		)
	}

	if strings.TrimSpace(recorder.Body.String()) == "" {
		t.Error("expected an error response body")
	}
}

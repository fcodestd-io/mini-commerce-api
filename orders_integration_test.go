package main

import (
	"context"
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

func TestOrderByIDEnforcesOwnership(t *testing.T) {
	// Load .env if present. Existing environment variables take precedence.
	_ = godotenv.Load()

	testDatabaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if testDatabaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping integration test")
	}

	// Prevent accidentally running this test against the development database.
	developmentDatabaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if developmentDatabaseURL != "" &&
		testDatabaseURL == developmentDatabaseURL {
		t.Fatal("TEST_DATABASE_URL must be different from DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()

	pool, err := OpenDB(testDatabaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	// Create unique test users.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	emailA := "order-owner-" + suffix + "@example.test"
	emailB := "order-other-" + suffix + "@example.test"

	var userAID, userBID int64

	err = pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash)
		VALUES ('Order Owner', $1, 'integration-test-hash')
		RETURNING id
	`, emailA).Scan(&userAID)
	if err != nil {
		t.Fatalf("create user A: %v", err)
	}

	err = pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password_hash)
		VALUES ('Other User', $1, 'integration-test-hash')
		RETURNING id
	`, emailB).Scan(&userBID)
	if err != nil {
		_, _ = pool.Exec(
			context.Background(),
			`DELETE FROM users WHERE id = $1`,
			userAID,
		)
		t.Fatalf("create user B: %v", err)
	}

	var orderID int64

	err = pool.QueryRow(ctx, `
		INSERT INTO orders (user_id, status, total)
		VALUES ($1, 'pending', 150000)
		RETURNING id
	`, userAID).Scan(&orderID)
	if err != nil {
		_, _ = pool.Exec(
			context.Background(),
			`DELETE FROM users WHERE id IN ($1, $2)`,
			userAID,
			userBID,
		)
		t.Fatalf("create test order: %v", err)
	}

	// Clean up only the rows created by this test.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		_, err := pool.Exec(
			cleanupCtx,
			`DELETE FROM orders WHERE id = $1`,
			orderID,
		)
		if err != nil {
			t.Errorf("clean up test order: %v", err)
		}

		_, err = pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id IN ($1, $2)`,
			userAID,
			userBID,
		)
		if err != nil {
			t.Errorf("clean up test users: %v", err)
		}
	}()

	handler := orderByIDHandler(pool)

	tests := []struct {
		name           string
		userID         int64
		orderID        string
		expectedStatus int
	}{
		{
			name:           "owner can access own order",
			userID:         userAID,
			orderID:        fmt.Sprintf("%d", orderID),
			expectedStatus: http.StatusOK,
		},
		{
			name:           "another user cannot access order",
			userID:         userBID,
			orderID:        fmt.Sprintf("%d", orderID),
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "nonexistent order returns not found",
			userID:         userAID,
			orderID:        fmt.Sprintf("%d", orderID+999999999),
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/orders/"+tt.orderID,
				nil,
			)
			req.SetPathValue("orderID", tt.orderID)

			req = req.WithContext(context.WithValue(
				req.Context(),
				userIDContextKey,
				tt.userID,
			))

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			if recorder.Code != tt.expectedStatus {
				t.Errorf(
					"expected status %d, got %d; body: %s",
					tt.expectedStatus,
					recorder.Code,
					recorder.Body.String(),
				)
			}
		})
	}
}

// Compile-time check that the test uses the expected pool type.
var _ *pgxpool.Pool

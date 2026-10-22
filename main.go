package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

func main() {
	config, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	dbPool, err := OpenDB(config.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer dbPool.Close()

	log.Println("Database connection established")

	migrationCtx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)

	if err := RunMigrations(migrationCtx, dbPool); err != nil {
		cancel()
		log.Fatal(err)
	}
	cancel()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /health/db", databaseHealthHandler(dbPool))
	mux.HandleFunc("POST /api/v1/demo/echo", echoHandler)

	mux.HandleFunc("GET /api/v1/categories", categoryHandler(dbPool))
	mux.HandleFunc("POST /api/v1/categories", categoryHandler(dbPool))

	mux.HandleFunc("GET /api/v1/products", productsHandler(dbPool))
	mux.HandleFunc("POST /api/v1/products", productsHandler(dbPool))
	mux.HandleFunc("GET /api/v1/products/{id}", productByIDHandler(dbPool))
	mux.HandleFunc("PUT /api/v1/products/{id}", productByIDHandler(dbPool))
	mux.HandleFunc("DELETE /api/v1/products/{id}", productByIDHandler(dbPool))

	mux.HandleFunc(
		"POST /api/v1/auth/register",
		registerHandler(dbPool, config.JWTSecret),
	)
	mux.HandleFunc(
		"POST /api/v1/auth/login",
		loginHandler(dbPool, config.JWTSecret),
	)
	mux.Handle(
		"GET /api/v1/me",
		authMiddleware(config.JWTSecret, meHandler(dbPool)),
	)

	mux.Handle(
		"GET /api/v1/cart",
		authMiddleware(config.JWTSecret, cartHandler(dbPool)),
	)
	mux.Handle(
		"POST /api/v1/cart/items",
		authMiddleware(config.JWTSecret, cartItemsHandler(dbPool)),
	)
	mux.Handle(
		"PUT /api/v1/cart/items/{productID}",
		authMiddleware(
			config.JWTSecret,
			cartItemByProductIDHandler(dbPool),
		),
	)
	mux.Handle(
		"DELETE /api/v1/cart/items/{productID}",
		authMiddleware(
			config.JWTSecret,
			cartItemByProductIDHandler(dbPool),
		),
	)

	mux.Handle(
		"POST /api/v1/checkout",
		authMiddleware(config.JWTSecret, checkoutHandler(dbPool)),
	)
	mux.Handle(
		"GET /api/v1/orders",
		authMiddleware(config.JWTSecret, ordersHandler(dbPool)),
	)
	mux.Handle(
		"GET /api/v1/orders/{orderID}",
		authMiddleware(config.JWTSecret, orderByIDHandler(dbPool)),
	)

	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf(
		"Mini Commerce API running on http://localhost:%s",
		config.Port,
	)

	if err := server.ListenAndServe(); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

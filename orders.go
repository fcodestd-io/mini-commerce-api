package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OrderItem struct {
	ProductID   int64  `json:"product_id"`
	ProductName string `json:"product_name"`
	UnitPrice   int64  `json:"unit_price"`
	Quantity    int    `json:"quantity"`
	LineTotal   int64  `json:"line_total"`
}

type OrderSummary struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	Total     int64     `json:"total"`
	CreatedAt time.Time `json:"created_at"`
}

type OrderDetail struct {
	OrderSummary
	Items []OrderItem `json:"items"`
}

type CheckoutResponse struct {
	Message string      `json:"message"`
	Order   OrderDetail `json:"order"`
}

type checkoutProduct struct {
	ProductID   int64
	ProductName string
	Price       int64
	Stock       int
	Quantity    int
}

func checkoutHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := getAuthenticatedUserID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to start checkout",
			)
			return
		}
		defer tx.Rollback(r.Context())

		var cartID int64

		err = tx.QueryRow(r.Context(), `
			SELECT id
			FROM carts
			WHERE user_id = $1
			FOR UPDATE
		`, userID).Scan(&cartID)

		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "Cart is empty")
			return
		}
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to retrieve cart",
			)
			return
		}

		rows, err := tx.Query(r.Context(), `
			SELECT
				p.id,
				p.name,
				p.price,
				p.stock,
				ci.quantity
			FROM cart_items ci
			JOIN products p ON p.id = ci.product_id
			WHERE ci.cart_id = $1
			ORDER BY p.id
			FOR UPDATE OF p
		`, cartID)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to validate cart",
			)
			return
		}

		products := make([]checkoutProduct, 0)

		for rows.Next() {
			var product checkoutProduct

			if err := rows.Scan(
				&product.ProductID,
				&product.ProductName,
				&product.Price,
				&product.Stock,
				&product.Quantity,
			); err != nil {
				rows.Close()
				writeError(
					w,
					http.StatusInternalServerError,
					"Failed to read cart",
				)
				return
			}

			products = append(products, product)
		}

		rowsErr := rows.Err()
		rows.Close()

		if rowsErr != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to read cart",
			)
			return
		}

		if len(products) == 0 {
			writeError(w, http.StatusBadRequest, "Cart is empty")
			return
		}

		var total int64

		for _, product := range products {
			if product.Quantity <= 0 || product.Stock < product.Quantity {
				writeError(
					w,
					http.StatusConflict,
					"Insufficient stock for product: "+product.ProductName,
				)
				return
			}

			if product.Price < 0 ||
				int64(product.Quantity) > 0 &&
					product.Price > (int64(^uint64(0)>>1)-total)/int64(product.Quantity) {
				writeError(
					w,
					http.StatusBadRequest,
					"Order total is too large",
				)
				return
			}

			total += product.Price * int64(product.Quantity)
		}

		var order OrderSummary

		err = tx.QueryRow(r.Context(), `
			INSERT INTO orders (user_id, status, total)
			VALUES ($1, 'pending', $2)
			RETURNING id, status, total, created_at
		`, userID, total).Scan(
			&order.ID,
			&order.Status,
			&order.Total,
			&order.CreatedAt,
		)

		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to create order",
			)
			return
		}

		orderItems := make([]OrderItem, 0, len(products))

		for _, product := range products {
			lineTotal := product.Price * int64(product.Quantity)

			_, err = tx.Exec(r.Context(), `
				INSERT INTO order_items (
					order_id,
					product_id,
					product_name,
					unit_price,
					quantity,
					line_total
				)
				VALUES ($1, $2, $3, $4, $5, $6)
			`,
				order.ID,
				product.ProductID,
				product.ProductName,
				product.Price,
				product.Quantity,
				lineTotal,
			)

			if err != nil {
				writeError(
					w,
					http.StatusInternalServerError,
					"Failed to save order items",
				)
				return
			}

			tag, err := tx.Exec(r.Context(), `
				UPDATE products
				SET stock = stock - $1,
				    updated_at = NOW()
				WHERE id = $2 AND stock >= $1
			`, product.Quantity, product.ProductID)

			if err != nil {
				writeError(
					w,
					http.StatusInternalServerError,
					"Failed to update product stock",
				)
				return
			}

			if tag.RowsAffected() != 1 {
				writeError(
					w,
					http.StatusConflict,
					"Insufficient stock for product: "+product.ProductName,
				)
				return
			}

			orderItems = append(orderItems, OrderItem{
				ProductID:   product.ProductID,
				ProductName: product.ProductName,
				UnitPrice:   product.Price,
				Quantity:    product.Quantity,
				LineTotal:   lineTotal,
			})
		}

		_, err = tx.Exec(r.Context(), `
			DELETE FROM cart_items
			WHERE cart_id = $1
		`, cartID)

		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to clear cart",
			)
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Checkout failed",
			)
			return
		}

		writeJSON(w, http.StatusCreated, CheckoutResponse{
			Message: "Checkout successful",
			Order: OrderDetail{
				OrderSummary: order,
				Items:        orderItems,
			},
		})
	}
}

func ordersHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := getAuthenticatedUserID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		rows, err := pool.Query(r.Context(), `
			SELECT id, status, total, created_at
			FROM orders
			WHERE user_id = $1
			ORDER BY created_at DESC, id DESC
		`, userID)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to retrieve orders",
			)
			return
		}
		defer rows.Close()

		orders := make([]OrderSummary, 0)

		for rows.Next() {
			var order OrderSummary

			if err := rows.Scan(
				&order.ID,
				&order.Status,
				&order.Total,
				&order.CreatedAt,
			); err != nil {
				writeError(
					w,
					http.StatusInternalServerError,
					"Failed to read orders",
				)
				return
			}

			orders = append(orders, order)
		}

		if err := rows.Err(); err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to read orders",
			)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"orders": orders,
		})
	}
}

func orderByIDHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := getAuthenticatedUserID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		orderID, err := strconv.ParseInt(r.PathValue("orderID"), 10, 64)
		if err != nil || orderID <= 0 {
			writeError(w, http.StatusBadRequest, "Invalid order ID")
			return
		}

		var order OrderSummary

		err = pool.QueryRow(r.Context(), `
			SELECT id, status, total, created_at
			FROM orders
			WHERE id = $1 AND user_id = $2
		`, orderID, userID).Scan(
			&order.ID,
			&order.Status,
			&order.Total,
			&order.CreatedAt,
		)

		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Order not found")
			return
		}
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to retrieve order",
			)
			return
		}

		rows, err := pool.Query(r.Context(), `
			SELECT product_id, product_name, unit_price, quantity, line_total
			FROM order_items
			WHERE order_id = $1
			ORDER BY id
		`, order.ID)

		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to retrieve order items",
			)
			return
		}
		defer rows.Close()

		items := make([]OrderItem, 0)

		for rows.Next() {
			var item OrderItem

			if err := rows.Scan(
				&item.ProductID,
				&item.ProductName,
				&item.UnitPrice,
				&item.Quantity,
				&item.LineTotal,
			); err != nil {
				writeError(
					w,
					http.StatusInternalServerError,
					"Failed to read order items",
				)
				return
			}

			items = append(items, item)
		}

		if err := rows.Err(); err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to read order items",
			)
			return
		}

		writeJSON(w, http.StatusOK, OrderDetail{
			OrderSummary: order,
			Items:        items,
		})
	}
}

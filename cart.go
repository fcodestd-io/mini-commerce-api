package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AddCartItemRequest struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type UpdateCartItemRequest struct {
	Quantity int `json:"quantity"`
}

type CartItem struct {
	ProductID   int64     `json:"product_id"`
	ProductName string    `json:"product_name"`
	Price       int64     `json:"price"`
	Quantity    int       `json:"quantity"`
	LineTotal   int64     `json:"line_total"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CartResponse struct {
	CartID int64      `json:"cart_id"`
	Items  []CartItem `json:"items"`
	Total  int64      `json:"total"`
}

func getAuthenticatedUserID(r *http.Request) (int64, bool) {
	userID, ok := r.Context().Value(userIDContextKey).(int64)
	return userID, ok && userID > 0
}

func getOrCreateCartID(
	pool *pgxpool.Pool,
	userID int64,
) (int64, error) {
	var cartID int64

	err := pool.QueryRow(
		contextBackground(),
		`
			INSERT INTO carts (user_id)
			VALUES ($1)
			ON CONFLICT (user_id)
			DO UPDATE SET user_id = EXCLUDED.user_id
			RETURNING id
		`,
		userID,
	).Scan(&cartID)

	return cartID, err
}

func contextBackground() context.Context {
	return context.Background()
}

func getOrCreateCartIDTx(
	tx pgx.Tx,
	userID int64,
) (int64, error) {
	var cartID int64

	err := tx.QueryRow(
		contextBackground(),
		`
			INSERT INTO carts (user_id)
			VALUES ($1)
			ON CONFLICT (user_id)
			DO UPDATE SET user_id = EXCLUDED.user_id
			RETURNING id
		`,
		userID,
	).Scan(&cartID)

	return cartID, err
}

func cartHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := getAuthenticatedUserID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		cartID, err := getOrCreateCartID(pool, userID)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to retrieve cart",
			)
			return
		}

		rows, err := pool.Query(r.Context(), `
			SELECT
				p.id,
				p.name,
				p.price,
				ci.quantity,
				p.price * ci.quantity,
				ci.updated_at
			FROM cart_items ci
			JOIN products p ON p.id = ci.product_id
			WHERE ci.cart_id = $1
			ORDER BY ci.id
		`, cartID)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to retrieve cart items",
			)
			return
		}
		defer rows.Close()

		items := make([]CartItem, 0)
		var total int64

		for rows.Next() {
			var item CartItem

			if err := rows.Scan(
				&item.ProductID,
				&item.ProductName,
				&item.Price,
				&item.Quantity,
				&item.LineTotal,
				&item.UpdatedAt,
			); err != nil {
				writeError(
					w,
					http.StatusInternalServerError,
					"Failed to read cart items",
				)
				return
			}

			items = append(items, item)
			total += item.LineTotal
		}

		if err := rows.Err(); err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to read cart items",
			)
			return
		}

		writeJSON(w, http.StatusOK, CartResponse{
			CartID: cartID,
			Items:  items,
			Total:  total,
		})
	}
}

func cartItemsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := getAuthenticatedUserID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		var input AddCartItemRequest
		if err := decodeJSON(w, r, &input); err != nil {
			writeDecodeError(w, err)
			return
		}

		if input.ProductID <= 0 || input.Quantity <= 0 {
			writeError(
				w,
				http.StatusBadRequest,
				"product_id and quantity must be positive",
			)
			return
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to start transaction",
			)
			return
		}
		defer tx.Rollback(r.Context())

		var stock int
		err = tx.QueryRow(r.Context(), `
			SELECT stock
			FROM products
			WHERE id = $1
			FOR UPDATE
		`, input.ProductID).Scan(&stock)

		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Product not found")
			return
		}
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to check product stock",
			)
			return
		}

		cartID, err := getOrCreateCartIDTx(tx, userID)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to retrieve cart",
			)
			return
		}

		var currentQuantity int
		err = tx.QueryRow(r.Context(), `
			SELECT quantity
			FROM cart_items
			WHERE cart_id = $1 AND product_id = $2
		`, cartID, input.ProductID).Scan(&currentQuantity)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to check cart item",
			)
			return
		}

		if input.Quantity > stock-currentQuantity {
			writeError(
				w,
				http.StatusConflict,
				"Requested quantity exceeds available stock",
			)
			return
		}

		_, err = tx.Exec(r.Context(), `
			INSERT INTO cart_items (cart_id, product_id, quantity)
			VALUES ($1, $2, $3)
			ON CONFLICT (cart_id, product_id)
			DO UPDATE SET
				quantity = cart_items.quantity + EXCLUDED.quantity,
				updated_at = NOW()
		`, cartID, input.ProductID, input.Quantity)
		if err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to add product to cart",
			)
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			writeError(
				w,
				http.StatusInternalServerError,
				"Failed to save cart",
			)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]string{
			"message": "Product added to cart",
		})
	}
}

func cartItemByProductIDHandler(
	pool *pgxpool.Pool,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := getAuthenticatedUserID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		productID, err := strconv.ParseInt(
			r.PathValue("productID"),
			10,
			64,
		)
		if err != nil || productID <= 0 {
			writeError(w, http.StatusBadRequest, "Invalid product ID")
			return
		}

		switch r.Method {
		case http.MethodPut:
			updateCartItem(pool, w, r, userID, productID)
		case http.MethodDelete:
			deleteCartItem(pool, w, r, userID, productID)
		default:
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	}
}

func updateCartItem(
	pool *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
	userID int64,
	productID int64,
) {
	var input UpdateCartItemRequest

	if err := decodeJSON(w, r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}

	if input.Quantity <= 0 {
		writeError(
			w,
			http.StatusBadRequest,
			"quantity must be greater than zero",
		)
		return
	}

	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"Failed to start transaction",
		)
		return
	}
	defer tx.Rollback(r.Context())

	var stock int
	err = tx.QueryRow(r.Context(), `
		SELECT stock
		FROM products
		WHERE id = $1
		FOR UPDATE
	`, productID).Scan(&stock)

	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Product not found")
		return
	}
	if err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"Failed to check product stock",
		)
		return
	}

	if input.Quantity > stock {
		writeError(
			w,
			http.StatusConflict,
			"Requested quantity exceeds available stock",
		)
		return
	}

	tag, err := tx.Exec(r.Context(), `
		UPDATE cart_items ci
		SET quantity = $1, updated_at = NOW()
		FROM carts c
		WHERE ci.cart_id = c.id
		  AND c.user_id = $2
		  AND ci.product_id = $3
	`, input.Quantity, userID, productID)

	if err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"Failed to update cart item",
		)
		return
	}

	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "Cart item not found")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"Failed to save cart",
		)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Cart item updated",
	})
}

func deleteCartItem(
	pool *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
	userID int64,
	productID int64,
) {
	tag, err := pool.Exec(r.Context(), `
		DELETE FROM cart_items ci
		USING carts c
		WHERE ci.cart_id = c.id
		  AND c.user_id = $1
		  AND ci.product_id = $2
	`, userID, productID)

	if err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"Failed to remove cart item",
		)
		return
	}

	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "Cart item not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Product removed from cart",
	})
}

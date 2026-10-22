package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOrderByIDRejectsInvalidOrderID(t *testing.T) {
	tests := []struct {
		name    string
		orderID string
	}{
		{name: "empty ID", orderID: ""},
		{name: "non numeric ID", orderID: "abc"},
		{name: "zero ID", orderID: "0"},
		{name: "negative ID", orderID: "-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/orders/"+tt.orderID,
				nil,
			)

			req.SetPathValue("orderID", tt.orderID)

			ctx := context.WithValue(
				req.Context(),
				userIDContextKey,
				int64(1),
			)
			req = req.WithContext(ctx)

			recorder := httptest.NewRecorder()

			handler := orderByIDHandler(nil)
			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf(
					"expected status %d, got %d; body: %s",
					http.StatusBadRequest,
					recorder.Code,
					recorder.Body.String(),
				)
			}
		})
	}
}

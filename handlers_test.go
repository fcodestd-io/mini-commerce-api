package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateProductRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name           string
		contentType    string
		body           string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "empty product name",
			contentType:    "application/json",
			body:           `{"name":"   ","price":75000,"stock":10}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "malformed JSON",
			contentType:    "application/json",
			body:           `{"name":`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid JSON or unknown field",
		},
		{
			name:           "missing product name",
			contentType:    "application/json",
			body:           `{"price":75000,"stock":10}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "negative price",
			contentType:    "application/json",
			body:           `{"name":"Mouse","price":-1,"stock":10}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "negative stock",
			contentType:    "application/json",
			body:           `{"name":"Mouse","price":75000,"stock":-1}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "wrong content type",
			contentType:    "text/plain",
			body:           `{"name":"Mouse","price":75000,"stock":10}`,
			expectedStatus: http.StatusUnsupportedMediaType,
			expectedError:  "Content-Type must be application/json",
		},
		{
			name:           "request body exceeds 1 MB",
			contentType:    "application/json",
			body:           `{"name":"` + strings.Repeat("a", 1<<20) + `"}`,
			expectedStatus: http.StatusRequestEntityTooLarge,
			expectedError:  "Request body must not exceed 1 MB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/products",
				strings.NewReader(tt.body),
			)
			req.Header.Set("Content-Type", tt.contentType)

			recorder := httptest.NewRecorder()

			createProduct(recorder, req, nil)

			if recorder.Code != tt.expectedStatus {
				t.Fatalf(
					"expected status %d, got %d; body: %s",
					tt.expectedStatus,
					recorder.Code,
					recorder.Body.String(),
				)
			}

			if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("expected JSON Content-Type, got %q", got)
			}

			var response ErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("expected valid JSON error response, got: %v", err)
			}

			if strings.TrimSpace(response.Error) == "" {
				t.Fatal("expected a non-empty error message")
			}

			if tt.expectedError != "" && response.Error != tt.expectedError {
				t.Errorf(
					"expected error %q, got %q",
					tt.expectedError,
					response.Error,
				)
			}
		})
	}
}

package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type HealthResponse struct {
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type EchoRequest struct {
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type EchoResponse struct {
	Message  string `json:"message"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	response := HealthResponse{
		Status:    "ok",
		Message:   "Mini Commerce API is running",
		Timestamp: time.Now().UTC(),
	}

	writeJSON(w, http.StatusOK, response)
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	contentType := r.Header.Get("Content-Type")
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])

	if mediaType != "application/json" {
		writeError(
			w,
			http.StatusUnsupportedMediaType,
			"Content-Type must be application/json",
		)
		return
	}

	var request EchoRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&request); err != nil {
		writeDecodeError(w, err)
		return
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			writeError(
				w,
				http.StatusBadRequest,
				"Request must contain only one JSON object",
			)
			return
		}

		writeDecodeError(w, err)
		return
	}

	request.Name = strings.TrimSpace(request.Name)

	if request.Name == "" {
		writeError(
			w,
			http.StatusBadRequest,
			"name must not be empty",
		)
		return
	}

	if request.Quantity <= 0 {
		writeError(
			w,
			http.StatusBadRequest,
			"quantity must be greater than zero",
		)
		return
	}

	response := EchoResponse{
		Message:  "Request received successfully",
		Name:     request.Name,
		Quantity: request.Quantity,
	}

	writeJSON(w, http.StatusOK, response)
}


func writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	var contentErr *contentTypeError

	switch {
	case errors.As(err, &maxBytesError):
		writeError(
			w,
			http.StatusRequestEntityTooLarge,
			"Request body must not exceed 1 MB",
		)

	case errors.As(err, &contentErr):
		writeError(
			w,
			http.StatusUnsupportedMediaType,
			contentErr.message,
		)

	case errors.Is(err, io.EOF):
		writeError(
			w,
			http.StatusBadRequest,
			"Request body must not be empty",
		)

	default:
		writeError(
			w,
			http.StatusBadRequest,
			"Invalid JSON or unknown field",
		)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, ErrorResponse{
		Error: message,
	})
}

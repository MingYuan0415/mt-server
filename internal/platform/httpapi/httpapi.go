// Package httpapi contains the stable JSON transport helpers.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type requestIDKey struct{}

// ErrorResponse is the public error envelope.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// APIError is a stable machine-readable API error.
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteJSON writes a no-store JSON response.
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// WriteError writes the standard error shape.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, status, ErrorResponse{Error: APIError{
		Code:      code,
		Message:   message,
		RequestID: RequestID(r.Context()),
	}})
}

// RequestContext adds a request ID, access logging, and panic isolation.
func RequestContext(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		w.Header().Set("X-Request-ID", requestID)

		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("request panic", "request_id", requestID, "path", r.URL.Path)
				WriteError(w, r.WithContext(ctx), http.StatusInternalServerError,
					"internal_error", "internal server error")
			}
			logger.Info("request completed",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"duration_ms", time.Since(started).Milliseconds())
		}()

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestID returns the current request ID.
func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(value[:])
}

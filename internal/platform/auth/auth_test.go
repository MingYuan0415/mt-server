package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
)

func TestMiddlewareAuthenticatesBearerToken(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	handler := New(token).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.DeviceID != "default" {
			t.Fatal("missing principal")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/api/v1/test", nil)
	request.Header.Set("Authorization", "bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", recorder.Code)
	}
}

func TestMiddlewareRejectsInvalidCredentials(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.RequestContext(logger, New(strings.Repeat("a", 32)).Wrap(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("protected handler called")
		})))

	for _, authorization := range []string{"", "Basic value", "Bearer wrong", "Bearer a b"} {
		request := httptest.NewRequest(http.MethodGet, "https://api.example.com/api/v1/test", nil)
		request.Header.Set("Authorization", authorization)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q returned %d", authorization, recorder.Code)
		}
		if recorder.Header().Get("WWW-Authenticate") != "Bearer" ||
			recorder.Header().Get("X-Request-ID") == "" ||
			!strings.Contains(recorder.Body.String(), "unauthorized") {
			t.Fatalf("incomplete error response: headers=%v body=%s",
				recorder.Header(), recorder.Body.String())
		}
	}
}

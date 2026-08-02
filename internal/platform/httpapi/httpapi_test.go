package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestContextAssignsRequestID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := RequestContext(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Fatal("request ID missing from context")
		}
		WriteError(w, r, http.StatusBadRequest, "bad_request", "bad request")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil))
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("X-Request-ID") == "" ||
		recorder.Header().Get("Cache-Control") != "private, no-store" ||
		!strings.Contains(recorder.Body.String(), recorder.Header().Get("X-Request-ID")) {
		t.Fatalf("unexpected response %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestRequestContextRecoversPanics(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := RequestContext(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("test panic")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(recorder.Body.String(), "internal_error") {
		t.Fatalf("unexpected panic response %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestRequestContextLogsStatusBytesAndDoesNotAppendAfterCommit(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := RequestContext(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("after commit")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/committed", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "partial" {
		t.Fatalf("panic after commit damaged response: %d %q", recorder.Code, recorder.Body.String())
	}
	logValue := logs.String()
	if !strings.Contains(logValue, "status=200") || !strings.Contains(logValue, "response_bytes=7") ||
		!strings.Contains(logValue, "response_committed=true") {
		t.Fatalf("request completion log missing fields: %s", logValue)
	}
}

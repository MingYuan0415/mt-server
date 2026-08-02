package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("MT_HEALTHCHECK_URL", server.URL)
	if err := healthcheck(); err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheckRejectsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("MT_HEALTHCHECK_URL", server.URL)
	if err := healthcheck(); err == nil {
		t.Fatal("expected healthcheck failure")
	}
}

func TestLoggerLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "other"} {
		if logger := newLogger(level); logger == nil {
			t.Fatalf("nil logger for %q", level)
		}
	}
}

package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MingYuan0415/mt-server/internal/platform"
)

type fakeModule struct {
	name  string
	ready error
}

func (f *fakeModule) Name() string                  { return f.name }
func (f *fakeModule) RegisterRoutes(*http.ServeMux) {}
func (f *fakeModule) Start(context.Context) error   { return nil }
func (f *fakeModule) Ready() error                  { return f.ready }
func (f *fakeModule) Close(context.Context) error   { return nil }

func TestHealthEndpoints(t *testing.T) {
	module := &fakeModule{name: "weather"}
	mux := http.NewServeMux()
	New("test-version", []platform.Module{module}).RegisterRoutes(mux)

	live := httptest.NewRecorder()
	mux.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK || live.Body.String() == "" {
		t.Fatalf("unexpected liveness response %d %s", live.Code, live.Body.String())
	}

	ready := httptest.NewRecorder()
	mux.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("unexpected readiness response %d %s", ready.Code, ready.Body.String())
	}

	module.ready = errors.New("circuit open")
	unavailable := httptest.NewRecorder()
	mux.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected unavailable response %d %s", unavailable.Code, unavailable.Body.String())
	}
}

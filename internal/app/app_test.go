package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/config"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

func TestNewStartsUnconfiguredManagementPlane(t *testing.T) {
	application, err := New(config.Config{
		ListenAddr: ":0", LogLevel: "info", StateDir: t.TempDir(),
		AdminAllowInsecureHTTP: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := application.closeModules(application.modules); err != nil {
			t.Error(err)
		}
		if err := application.lock.Close(); err != nil {
			t.Error(err)
		}
	}()

	for _, test := range []struct {
		path   string
		status int
		body   string
	}{
		{path: "/health/live", status: http.StatusOK, body: `"version":"test"`},
		{path: "/health/ready", status: http.StatusServiceUnavailable, body: "setup_required"},
		{path: "/api/v1/weather/current", status: http.StatusServiceUnavailable, body: "service_unconfigured"},
		{path: "/admin/", status: http.StatusOK, body: "mt-server"},
		{path: "/admin/missing", status: http.StatusNotFound, body: "not_found"},
		{path: "/admin/assets/missing.js", status: http.StatusNotFound, body: "not_found"},
		{path: "/admin/api/v1/missing", status: http.StatusNotFound, body: "not_found"},
	} {
		recorder := httptest.NewRecorder()
		application.server.Handler.ServeHTTP(recorder,
			httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.body) {
			t.Fatalf("%s returned %d %s", test.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestNewRestoresConfiguredRuntimeWithoutCallingUpstream(t *testing.T) {
	directory := t.TempDir()
	store, err := state.NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := validRuntimeState(t)
	value.UpdatedAt = now
	if _, err := store.CommitInitial(value); err != nil {
		t.Fatal(err)
	}
	application, err := New(config.Config{
		ListenAddr: ":0", LogLevel: "info", StateDir: directory,
		AdminAllowInsecureHTTP: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := application.closeModules(application.modules); err != nil {
			t.Error(err)
		}
		if err := application.lock.Close(); err != nil {
			t.Error(err)
		}
	}()

	ready := httptest.NewRecorder()
	application.server.Handler.ServeHTTP(ready,
		httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"weather":"ready"`) {
		t.Fatalf("restored runtime returned %d %s", ready.Code, ready.Body.String())
	}
	device := httptest.NewRecorder()
	application.server.Handler.ServeHTTP(device,
		httptest.NewRequest(http.MethodGet, "/api/v1/weather/current", nil))
	if device.Code != http.StatusUnauthorized {
		t.Fatalf("restored device API returned %d %s", device.Code, device.Body.String())
	}
}

func TestNewRequiresPersistentOriginInProxyMode(t *testing.T) {
	directory := t.TempDir()
	store, err := state.NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	value := validRuntimeState(t)
	if _, err := store.CommitInitial(value); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ListenAddr: ":0", LogLevel: "info", StateDir: directory,
		AdminBehindHTTPSProxy: true,
	}
	if _, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"); err == nil ||
		!strings.Contains(err.Error(), "at least one HTTPS origin") {
		t.Fatalf("proxy mode accepted an empty persisted origin list: %v", err)
	}
	value.Admin.PublicOrigins = []string{"https://admin.example.com"}
	if _, err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer application.lock.Close()
}

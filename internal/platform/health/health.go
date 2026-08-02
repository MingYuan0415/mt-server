// Package health exposes process and module health without upstream probes.
package health

import (
	"errors"
	"net/http"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
)

// Handler reports liveness and readiness for registered modules.
type Handler struct {
	startedAt time.Time
	version   string
	modules   []platform.Module
}

// New constructs health endpoints.
func New(version string, modules []platform.Module) *Handler {
	return &Handler{startedAt: time.Now().UTC(), version: version, modules: modules}
}

// RegisterRoutes installs unauthenticated health endpoints.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health/live", h.live)
	mux.HandleFunc("GET /health/ready", h.ready)
}

func (h *Handler) live(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    h.version,
		"started_at": h.startedAt,
	})
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	statuses := make(map[string]string, len(h.modules))
	ready := true
	for _, module := range h.modules {
		if err := module.Ready(); err != nil {
			ready = false
			if errors.Is(err, platform.ErrSetupRequired) {
				statuses[module.Name()] = "setup_required"
			} else {
				statuses[module.Name()] = "unavailable"
			}
			continue
		}
		statuses[module.Name()] = "ready"
	}
	if !ready {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "unavailable",
			"modules": statuses,
		})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "ready",
		"modules": statuses,
	})
}

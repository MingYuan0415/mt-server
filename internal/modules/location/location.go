// Package locationmod exposes the authenticated device location endpoint.
package locationmod

import (
	"context"
	"errors"
	"net/http"

	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

// Response is the display-safe device-facing location response. It never
// contains the source IP or coordinates.
type Response struct {
	SchemaVersion    int            `json:"schema_version"`
	Location         PublicLocation `json:"location"`
	AccuracyRadiusKm *int           `json:"accuracy_radius_km,omitempty"`
}

// PublicLocation contains coarse display metadata only.
type PublicLocation struct {
	City      string `json:"city,omitempty"`
	Region    string `json:"region,omitempty"`
	Country   string `json:"country,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Source    string `json:"source"`
	Provider  string `json:"provider"`
	Precision string `json:"precision"`
}

// Module exposes the device location API through the shared module lifecycle.
type Module struct {
	source *location.Source
}

// NewModule constructs the location HTTP module.
func NewModule(source *location.Source) *Module {
	return &Module{source: source}
}

// Name returns the health registry name.
func (m *Module) Name() string { return "location" }

// RegisterRoutes installs the authenticated location endpoint.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/location", m.handle)
}

// Start has no eager work because location is request-specific.
func (m *Module) Start(_ context.Context) error { return nil }

// Ready reports that the endpoint can serve when the runtime is active.
func (m *Module) Ready() error { return nil }

// Close has no background work.
func (m *Module) Close(context.Context) error { return nil }

func (m *Module) handle(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.PrincipalFromContext(r.Context()); !ok {
		httpapi.WriteError(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required")
		return
	}
	point, resolved, err := m.source.EffectivePoint(r)
	if err != nil {
		switch {
		case errors.Is(err, location.ErrPartial):
			httpapi.WriteError(w, r, http.StatusBadRequest, "invalid_location",
				"device location headers must be provided all together or not at all")
		case errors.Is(err, location.ErrInvalid):
			httpapi.WriteError(w, r, http.StatusBadRequest,
				"invalid_location", "device location headers are invalid")
		default:
			httpapi.WriteError(w, r, http.StatusServiceUnavailable,
				"location_unavailable", "device location could not be determined")
		}
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, Response{
		SchemaVersion: 1,
		Location: PublicLocation{
			City:      point.City,
			Region:    point.Region,
			Country:   point.Country,
			Timezone:  point.Timezone,
			Source:    point.Source,
			Provider:  point.Provider,
			Precision: point.Precision,
		},
		AccuracyRadiusKm: resolved.AccuracyKm,
	})
}

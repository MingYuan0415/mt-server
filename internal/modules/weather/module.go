package weather

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

// Module exposes the weather service through the shared module lifecycle.
type Module struct {
	service *Service
	limiter *location.ChangeLimiter
	logger  *slog.Logger
}

// NewModule constructs the weather HTTP module.
func NewModule(service *Service, limiter *location.ChangeLimiter, logger *slog.Logger) *Module {
	return &Module{service: service, limiter: limiter, logger: logger}
}

// Name returns the health registry name.
func (m *Module) Name() string { return "weather" }

// RegisterRoutes installs versioned weather endpoints.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/weather/current", m.handle(KindCurrent))
	mux.HandleFunc("GET /api/v1/weather/hourly", m.handle(KindHourly))
	mux.HandleFunc("GET /api/v1/weather/daily", m.handle(KindDaily))
	mux.HandleFunc("GET /api/v1/weather/alerts", m.handle(KindAlerts))
}

// Start has no eager work because location is request-specific.
func (m *Module) Start(context.Context) error { return nil }

// Ready reports provider circuit state.
func (m *Module) Ready() error { return m.service.Ready() }

// Close stops background refreshes.
func (m *Module) Close(ctx context.Context) error { return m.service.Close(ctx) }

func (m *Module) handle(kind Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, r, http.StatusUnauthorized,
				"unauthorized", "authentication required")
			return
		}
		point, err := location.FromRequest(r)
		if err != nil {
			code := "invalid_location"
			message := "device location headers are invalid"
			if errors.Is(err, location.ErrRequired) {
				code = "location_required"
				message = "device location headers are required"
			}
			httpapi.WriteError(w, r, http.StatusBadRequest, code, message)
			return
		}
		if allowed, retry := m.limiter.Allow(principal.DeviceID, point.CacheKey()); !allowed {
			seconds := int64((retry + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			httpapi.WriteError(w, r, http.StatusTooManyRequests,
				"location_rate_limited", "device location changed too frequently")
			return
		}
		envelope, err := m.service.Get(r.Context(), kind, point)
		if err != nil {
			status := http.StatusServiceUnavailable
			code := "weather_unavailable"
			message := "weather data is temporarily unavailable"
			if errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
				code = "upstream_timeout"
				message = "weather provider timed out"
			}
			m.logger.Warn("weather request failed", "kind", kind,
				"request_id", httpapi.RequestID(r.Context()), "error", err)
			httpapi.WriteError(w, r, status, code, message)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, envelope)
	}
}

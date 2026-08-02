package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/admin"
	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

type runtime struct {
	mu    sync.Mutex
	ready bool
}

type preparedChange struct{ activate func() }

func (p *preparedChange) Activate() {
	if p.activate != nil {
		p.activate()
		p.activate = nil
	}
}

func (p *preparedChange) Discard() { p.activate = nil }

func (r *runtime) Test(_ context.Context, _ state.State,
	point *location.Point) (weather.Verification, string, error) {
	if point == nil {
		return weather.Verification{}, "", location.ErrRequired
	}
	now := time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC)
	return weather.Verification{
		Source: weather.Source{
			ID: "qweather", Name: "QWeather", AttributionURL: "https://www.qweather.com/",
		},
		Location: weather.PublicLocation{
			City: "Example", Source: "browser", Provider: "browser", Precision: "coarse",
		},
		TestedAt:  now,
		UpdatedAt: now.Add(-time.Minute),
		Data: weather.Current{
			ObservedAt: now.Add(-2 * time.Minute), TemperatureC: 28, FeelsLikeC: 31,
			ConditionCode: "101", ConditionText: "多云", HumidityPercent: 72,
			WindSpeedKMH: 8,
		},
	}, "test-public-key-fingerprint", nil
}

func (r *runtime) Prepare(state.State) (platform.PreparedChange, error) {
	return &preparedChange{activate: func() {
		r.mu.Lock()
		r.ready = true
		r.mu.Unlock()
	}}, nil
}

func (*runtime) PrepareTokens([]state.DeviceToken) (platform.PreparedChange, error) {
	return &preparedChange{}, nil
}

func (r *runtime) Ready() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready {
		return errors.New("setup required")
	}
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	store, err := state.NewStore(os.Getenv("MT_STATE_DIR"))
	if err != nil {
		logger.Error("open state", "error", err)
		os.Exit(1)
	}
	management, err := admin.New(store, &runtime{}, adminauth.NewSessions(),
		adminauth.NewTransportPolicy(true, false), logger, "web-test")
	if err != nil {
		logger.Error("create management handler", "error", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	management.RegisterRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, request *http.Request) {
		httpapi.WriteError(w, request, http.StatusNotFound, "not_found", "resource not found")
	})
	server := &http.Server{
		Addr:              "127.0.0.1:18080",
		Handler:           httpapi.RequestContext(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("server listening")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
}

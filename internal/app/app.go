// Package app composes and runs the modular service.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/admin"
	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/config"
	"github.com/MingYuan0415/mt-server/internal/platform/health"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

const shutdownTimeout = 10 * time.Second

// App owns the HTTP server and module lifecycle.
type App struct {
	server  *http.Server
	modules []platform.Module
	logger  *slog.Logger
}

// New opens persistent state and composes all enabled modules.
func New(cfg config.Config, logger *slog.Logger, version string) (*App, error) {
	store, err := state.NewStore(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("open application state: %w", err)
	}
	runtime := NewRuntimeManager(logger)
	persisted, err := store.Load()
	if err == nil {
		if err := runtime.Apply(persisted); err != nil {
			return nil, fmt.Errorf("activate application state: %w", err)
		}
	} else if !errors.Is(err, state.ErrNotInitialized) {
		return nil, err
	}
	modules := []platform.Module{runtime}

	rootMux := http.NewServeMux()
	for _, module := range modules {
		module.RegisterRoutes(rootMux)
	}
	health.New(version, modules).RegisterRoutes(rootMux)
	management, err := admin.New(
		store,
		runtime,
		adminauth.NewSessions(),
		adminauth.NewTransportPolicy(cfg.AdminAllowInsecureHTTP, cfg.AdminBehindHTTPSProxy,
			cfg.AdminPublicOrigins...),
		logger,
		version,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize management interface: %w", err)
	}
	management.RegisterRoutes(rootMux)
	rootMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
			return
		}
		httpapi.WriteError(w, r, http.StatusNotFound, "not_found", "resource not found")
	})

	return &App{
		server: &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           httpapi.RequestContext(logger, rootMux),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      35 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 * 1024,
		},
		modules: modules,
		logger:  logger,
	}, nil
}

// Run starts all modules and serves until cancellation or listener failure.
func (a *App) Run(ctx context.Context) error {
	started := make([]platform.Module, 0, len(a.modules))
	for _, module := range a.modules {
		if err := module.Start(ctx); err != nil {
			a.closeModules(started)
			return fmt.Errorf("start module %s: %w", module.Name(), err)
		}
		started = append(started, module)
	}

	serverError := make(chan error, 1)
	go func() {
		a.logger.Info("server listening")
		serverError <- a.server.ListenAndServe()
	}()

	var runError error
	select {
	case <-ctx.Done():
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			runError = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := a.server.Shutdown(shutdownCtx); err != nil && runError == nil {
		runError = fmt.Errorf("shutdown HTTP server: %w", err)
	}
	if err := a.closeModules(started); err != nil && runError == nil {
		runError = err
	}
	return runError
}

func (a *App) closeModules(modules []platform.Module) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var closeError error
	for index := len(modules) - 1; index >= 0; index-- {
		if err := modules[index].Close(shutdownCtx); err != nil && closeError == nil {
			closeError = fmt.Errorf("close module %s: %w", modules[index].Name(), err)
		}
	}
	return closeError
}

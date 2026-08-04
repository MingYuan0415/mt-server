package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MingYuan0415/mt-server/internal/app"
	"github.com/MingYuan0415/mt-server/internal/platform/config"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			if err := healthcheck(); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "admin-origin":
			if err := runAdminOrigin(os.Args[2:]); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, "admin origin error:", err)
				os.Exit(1)
			}
			return
		case "state":
			if err := runState(os.Args[2:]); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, "state error:", err)
				os.Exit(1)
			}
			return
		default:
			_, _ = fmt.Fprintln(os.Stderr, "unknown command")
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(1)
	}
	logger := newLogger(cfg.LogLevel)
	service, err := app.New(cfg, logger, version)
	if err != nil {
		logger.Error("application initialization failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := service.Run(ctx); err != nil {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func healthcheck() error {
	endpoint := os.Getenv("MT_HEALTHCHECK_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080/health/live"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned status %d", response.StatusCode)
	}
	return nil
}

func newLogger(levelName string) *slog.Logger {
	var level slog.Level
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

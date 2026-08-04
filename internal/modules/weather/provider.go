package weather

import (
	"context"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

// ProviderDiagnostics is a safe, non-blocking provider health snapshot.
type ProviderDiagnostics struct {
	Status       string     `json:"status"`
	BlockedUntil *time.Time `json:"blocked_until,omitempty"`
}

// Provider supplies normalized datasets for one location.
type Provider interface {
	Source() Source
	Fetch(context.Context, Kind, location.Point) (ProviderResult, error)
	Ready() error
	Diagnostics() ProviderDiagnostics
	Close() error
}

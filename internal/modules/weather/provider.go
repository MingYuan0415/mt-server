package weather

import (
	"context"

	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

// Provider supplies normalized datasets for one location.
type Provider interface {
	Source() Source
	Fetch(context.Context, Kind, location.Point) (ProviderResult, error)
	Ready() error
	Close() error
}

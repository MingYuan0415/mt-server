// Package platform contains shared application infrastructure.
package platform

import (
	"context"
	"errors"
	"net/http"
)

// ErrSetupRequired means the service is alive but not initialized.
var ErrSetupRequired = errors.New("setup required")

// Module is an explicitly wired service capability.
type Module interface {
	Name() string
	RegisterRoutes(*http.ServeMux)
	Start(context.Context) error
	Ready() error
	Close(context.Context) error
}

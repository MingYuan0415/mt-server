package app

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

func TestRuntimeTransitionsFromSetupToConfigured(t *testing.T) {
	runtime := NewRuntimeManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/weather/current", nil)
	recorder := httptest.NewRecorder()
	runtime.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable ||
		!errors.Is(runtime.Ready(), platform.ErrSetupRequired) {
		t.Fatalf("unexpected unconfigured runtime %d %v", recorder.Code, runtime.Ready())
	}

	value := validRuntimeState(t)
	if err := runtime.Apply(value); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Ready(); err != nil {
		t.Fatalf("configured runtime is not ready: %v", err)
	}
	unauthorized := httptest.NewRecorder()
	runtime.ServeHTTP(unauthorized,
		httptest.NewRequest(http.MethodGet, "/api/v1/weather/current", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected device auth response %d", unauthorized.Code)
	}

	missingLocationRequest := httptest.NewRequest(http.MethodGet, "/api/v1/weather/current", nil)
	missingLocationRequest.Header.Set("Authorization", "Bearer high-entropy-device-token-for-tests")
	missingLocation := httptest.NewRecorder()
	runtime.ServeHTTP(missingLocation, missingLocationRequest)
	if missingLocation.Code != http.StatusBadRequest ||
		!strings.Contains(missingLocation.Body.String(), "location_required") {
		t.Fatalf("unexpected location response %d %s", missingLocation.Code, missingLocation.Body.String())
	}

	value.DeviceTokens = append(value.DeviceTokens, state.DeviceToken{
		ID: "device_second", Name: "Second", Hash: auth.HashToken("another-high-entropy-device-token"),
		CreatedAt: time.Now().UTC(),
	})
	if err := runtime.ReplaceTokens(value.DeviceTokens); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRejectsInvalidPersistentState(t *testing.T) {
	value := validRuntimeState(t)
	value.QWeather.APIHost = "weather.example.com"
	if err := NewRuntimeManager(slog.Default()).Apply(value); err == nil {
		t.Fatal("expected invalid QWeather host error")
	}
}

func TestRuntimeRequiresTemporaryConfigurationTestLocation(t *testing.T) {
	value := validRuntimeState(t)
	runtime := NewRuntimeManager(slog.Default())
	if _, _, err := runtime.Test(context.Background(), value, nil); !errors.Is(err, location.ErrRequired) {
		t.Fatalf("expected missing test location error, got %v", err)
	}
}

func TestRuntimeRejectsInvalidTemporaryLocationBeforeUpstreamCall(t *testing.T) {
	value := validRuntimeState(t)
	runtime := NewRuntimeManager(slog.Default())
	for _, point := range []*location.Point{
		{Latitude: 91, Longitude: 114},
		{Latitude: 22, Longitude: 114, City: strings.Repeat("x", 129)},
		{Latitude: 22, Longitude: 114, Region: "bad\tregion"},
	} {
		if _, _, err := runtime.Test(context.Background(), value, point); !errors.Is(err, location.ErrInvalid) {
			t.Fatalf("expected invalid test location error, got %v", err)
		}
	}
}

func validRuntimeState(t *testing.T) state.State {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	password, err := adminauth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	return state.State{
		SchemaVersion: state.SchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Admin:         state.AdminState{Password: password},
		QWeather: state.QWeatherState{
			APIHost: "account.re.qweatherapi.com", ProjectID: "project",
			CredentialID:  "credential",
			PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})),
			Language:      "zh", Unit: "m", RequestTimeoutSeconds: 10, CircuitCooldownSeconds: 900,
		},
		Cache: state.DefaultCache(),
		DeviceTokens: []state.DeviceToken{{
			ID: "device_default", Name: "Default",
			Hash: auth.HashToken("high-entropy-device-token-for-tests"), CreatedAt: time.Now().UTC(),
		}},
	}
}

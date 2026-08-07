package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	locationmod "github.com/MingYuan0415/mt-server/internal/modules/location"
	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
	"github.com/MingYuan0415/mt-server/internal/providers/geoip"
	"github.com/MingYuan0415/mt-server/internal/providers/qweather"
)

const (
	maximumDeviceTokens   = 32
	locationBurstCapacity = 4
	locationRefillPeriod  = 5 * time.Minute
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type runtimeInstance struct {
	service *weather.Service
	apiMux  http.Handler
	handler http.Handler
}

type preparedRuntimeChange struct {
	manager   *RuntimeManager
	instance  *runtimeInstance
	deviceIDs []string
}

func (p *preparedRuntimeChange) Activate() {
	if p.instance == nil {
		return
	}
	p.manager.activate(p.instance, p.deviceIDs)
	p.instance = nil
}

func (p *preparedRuntimeChange) Discard() {
	if p.instance == nil {
		return
	}
	p.manager.closeService(p.instance.service, "discarded weather runtime")
	p.instance = nil
}

type preparedTokenChange struct {
	manager   *RuntimeManager
	handler   http.Handler
	deviceIDs []string
}

func (p *preparedTokenChange) Activate() {
	if p.handler == nil {
		return
	}
	p.manager.mu.Lock()
	if p.manager.current != nil {
		p.manager.current.handler = p.handler
	}
	p.manager.mu.Unlock()
	p.manager.limiter.Retain(p.deviceIDs)
	p.handler = nil
}

func (p *preparedTokenChange) Discard() { p.handler = nil }

// RuntimeManager owns the active weather, location, and device-auth snapshot.
type RuntimeManager struct {
	mu      sync.RWMutex
	current *runtimeInstance
	logger  *slog.Logger
	limiter *location.ChangeLimiter
	geoIP   *geoip.Store
	source  *location.Source
}

// NewRuntimeManager constructs an unconfigured runtime. geoIPDBPath enables
// IP-inference; trustedHeader and trustedNets configure the trusted-proxy
// contract for the client-IP header; cloudflareHeaders enables Cloudflare
// visitor-location headers as an inference source.
func NewRuntimeManager(logger *slog.Logger, geoIPDBPath, trustedHeader string,
	trustedNets []netip.Prefix, cloudflareHeaders bool) *RuntimeManager {
	var geoIPStore *geoip.Store
	if geoIPDBPath != "" {
		geoIPStore = geoip.New(geoIPDBPath, logger)
	}
	var source *location.Source
	if geoIPStore != nil || cloudflareHeaders {
		var cloudflare *location.Cloudflare
		if cloudflareHeaders {
			cloudflare = location.NewCloudflare(trustedNets)
		}
		source = location.NewSourceWithCloudflare(
			location.NewIPExtractor(trustedHeader, trustedNets), geoIPStore, cloudflare)
	} else {
		source = location.NewSource(nil, nil)
	}
	return &RuntimeManager{
		logger:  logger,
		limiter: location.NewChangeLimiter(locationBurstCapacity, locationRefillPeriod),
		geoIP:   geoIPStore,
		source:  source,
	}
}

// Name implements platform.Module.
func (m *RuntimeManager) Name() string { return "weather" }

// RegisterRoutes installs the stable device API boundary.
func (m *RuntimeManager) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/api/", m)
}

// Start implements platform.Module.
func (m *RuntimeManager) Start(context.Context) error {
	if m.geoIP != nil {
		m.geoIP.Start()
	}
	return nil
}

// Ready reports setup and provider circuit state without an upstream call.
func (m *RuntimeManager) Ready() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return platform.ErrSetupRequired
	}
	return m.current.service.Ready()
}

// Diagnostics returns the active runtime snapshot without provider I/O.
func (m *RuntimeManager) Diagnostics() (weather.Diagnostics, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return weather.Diagnostics{}, platform.ErrSetupRequired
	}
	return m.current.service.Diagnostics(), nil
}

// ServeHTTP dispatches through one consistent runtime snapshot.
func (m *RuntimeManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		httpapi.WriteError(w, r, http.StatusServiceUnavailable,
			"service_unconfigured", "service setup is required")
		return
	}
	m.current.handler.ServeHTTP(w, r)
}

// Test verifies a complete candidate with one uncached current-weather call.
func (m *RuntimeManager) Test(ctx context.Context, value state.State,
	testPoint *location.Point) (weather.Verification, string, error) {
	provider, _, _, err := m.buildComponents(value)
	if err != nil {
		return weather.Verification{}, "", err
	}
	defer provider.Close()
	if testPoint == nil {
		return weather.Verification{}, "", location.ErrRequired
	}
	point, err := location.Normalize(*testPoint)
	if err != nil {
		return weather.Verification{}, "", err
	}
	point.Source = "browser"
	point.Provider = "browser"
	point.Precision = "coarse"
	result, err := provider.Fetch(ctx, weather.KindCurrent, point)
	if err != nil {
		return weather.Verification{}, "", err
	}
	current, ok := result.Data.(weather.Current)
	if !ok {
		return weather.Verification{}, "", errors.New("QWeather current response has an unexpected type")
	}
	if _, err := provider.Fetch(ctx, weather.KindAlerts, point); err != nil {
		return weather.Verification{}, "", fmt.Errorf("verify QWeather alerts: %w", err)
	}
	fingerprint, err := qweather.PublicKeyFingerprint([]byte(value.QWeather.PrivateKeyPEM))
	if err != nil {
		return weather.Verification{}, "", err
	}
	return weather.Verification{
		Source: provider.Source(), Location: publicLocation(point), TestedAt: time.Now().UTC(),
		UpdatedAt: result.UpdatedAt, Data: current,
	}, fingerprint, nil
}

// Apply builds and atomically activates a complete runtime.
func (m *RuntimeManager) Apply(value state.State) error {
	prepared, err := m.Prepare(value)
	if err != nil {
		return err
	}
	prepared.Activate()
	return nil
}

// Prepare builds a complete runtime without changing the active snapshot.
func (m *RuntimeManager) Prepare(value state.State) (platform.PreparedChange, error) {
	provider, options, credentials, err := m.buildComponents(value)
	if err != nil {
		return nil, err
	}
	service := weather.NewService(provider, m.logger, options)
	module := weather.NewModule(service, m.limiter, m.source, m.logger)
	locationModule := locationmod.NewModule(m.source)
	apiMux := http.NewServeMux()
	module.RegisterRoutes(apiMux)
	locationModule.RegisterRoutes(apiMux)
	apiMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, r, http.StatusNotFound, "not_found", "resource not found")
	})
	return &preparedRuntimeChange{manager: m, instance: &runtimeInstance{
		service: service,
		apiMux:  apiMux,
		handler: auth.NewCredentials(credentials).Wrap(apiMux),
	}, deviceIDs: deviceIDs(value.DeviceTokens)}, nil
}

func (m *RuntimeManager) activate(instance *runtimeInstance, retainedDeviceIDs []string) {
	m.mu.Lock()
	previous := m.current
	m.current = instance
	m.mu.Unlock()
	m.limiter.Retain(retainedDeviceIDs)
	if previous != nil {
		m.closeService(previous.service, "old weather runtime")
	}
}

// ReplaceTokens updates device credentials without discarding weather cache.
func (m *RuntimeManager) ReplaceTokens(tokens []state.DeviceToken) error {
	prepared, err := m.PrepareTokens(tokens)
	if err != nil {
		return err
	}
	prepared.Activate()
	return nil
}

// PrepareTokens builds a device-auth snapshot without changing active credentials.
func (m *RuntimeManager) PrepareTokens(tokens []state.DeviceToken) (platform.PreparedChange, error) {
	credentials, err := credentialsFromState(tokens)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return nil, platform.ErrSetupRequired
	}
	return &preparedTokenChange{
		manager: m, handler: auth.NewCredentials(credentials).Wrap(m.current.apiMux),
		deviceIDs: deviceIDs(tokens),
	}, nil
}

// Close stops the active weather service and the geoip poller.
func (m *RuntimeManager) Close(ctx context.Context) error {
	m.mu.Lock()
	current := m.current
	m.current = nil
	m.mu.Unlock()
	if m.geoIP != nil {
		if err := m.geoIP.Close(); err != nil {
			m.logger.Warn("geoip store did not close cleanly", "error", err)
		}
	}
	if current == nil {
		return nil
	}
	return current.service.Close(ctx)
}

func (m *RuntimeManager) closeService(service *weather.Service, label string) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		m.logger.Warn(label+" did not close cleanly", "error", err)
	}
}

func (m *RuntimeManager) buildComponents(value state.State) (*qweather.Client,
	weather.CacheOptions, []auth.Credential, error) {
	if err := validateState(value); err != nil {
		return nil, weather.CacheOptions{}, nil, err
	}
	provider, err := qweather.New(
		"https://"+value.QWeather.APIHost,
		[]byte(value.QWeather.PrivateKeyPEM),
		value.QWeather.CredentialID,
		value.QWeather.ProjectID,
		value.QWeather.Language,
		value.QWeather.Unit,
		time.Duration(value.QWeather.RequestTimeoutSeconds)*time.Second,
		time.Duration(value.QWeather.CircuitCooldownSeconds)*time.Second,
	)
	if err != nil {
		return nil, weather.CacheOptions{}, nil, fmt.Errorf("create QWeather provider: %w", err)
	}
	credentials, err := credentialsFromState(value.DeviceTokens)
	if err != nil {
		_ = provider.Close()
		return nil, weather.CacheOptions{}, nil, err
	}
	options := weather.CacheOptions{
		CurrentTTL:      time.Duration(value.Cache.CurrentTTL) * time.Second,
		CurrentStaleMax: time.Duration(value.Cache.CurrentStaleMax) * time.Second,
		HourlyTTL:       time.Duration(value.Cache.HourlyTTL) * time.Second,
		HourlyStaleMax:  time.Duration(value.Cache.HourlyStaleMax) * time.Second,
		DailyTTL:        time.Duration(value.Cache.DailyTTL) * time.Second,
		DailyStaleMax:   time.Duration(value.Cache.DailyStaleMax) * time.Second,
		AlertsTTL:       time.Duration(value.Cache.AlertsTTL) * time.Second,
		AlertsStaleMax:  time.Duration(value.Cache.AlertsStaleMax) * time.Second,
		MaxLocations:    value.Cache.MaxLocations,
	}
	return provider, options, credentials, nil
}

func validateState(value state.State) error {
	if value.SchemaVersion != state.SchemaVersion {
		return errors.New("unsupported state schema")
	}
	if !adminauth.ValidPasswordHash(value.Admin.Password) {
		return errors.New("invalid administrator password verifier")
	}
	if err := validateQWeather(value.QWeather); err != nil {
		return err
	}
	if err := validateCache(value.Cache); err != nil {
		return err
	}
	_, err := credentialsFromState(value.DeviceTokens)
	return err
}

func validateQWeather(value state.QWeatherState) error {
	host := strings.ToLower(strings.TrimSpace(value.APIHost))
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/:@?# ") ||
		!strings.HasSuffix(host, ".qweatherapi.com") {
		return errors.New("QWeather API Host must be an account-specific qweatherapi.com hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("QWeather API Host is invalid")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return errors.New("QWeather API Host is invalid")
			}
		}
	}
	if !identifierPattern.MatchString(value.ProjectID) || !identifierPattern.MatchString(value.CredentialID) {
		return errors.New("QWeather project and credential IDs are invalid")
	}
	if value.Language != "zh" || value.Unit != "m" {
		return errors.New("v1 requires QWeather language zh and metric units")
	}
	if value.RequestTimeoutSeconds < 1 || value.RequestTimeoutSeconds > 60 ||
		value.CircuitCooldownSeconds < 60 || value.CircuitCooldownSeconds > 86400 {
		return errors.New("QWeather timeout policy is invalid")
	}
	if len(value.PrivateKeyPEM) == 0 || len(value.PrivateKeyPEM) > 32*1024 {
		return errors.New("QWeather private key is invalid")
	}
	_, err := qweather.PublicKeyFingerprint([]byte(value.PrivateKeyPEM))
	return err
}

func validateCache(value state.CacheState) error {
	if value.CurrentTTL <= 0 || value.HourlyTTL <= 0 || value.DailyTTL <= 0 ||
		value.CurrentStaleMax <= value.CurrentTTL ||
		value.HourlyStaleMax <= value.HourlyTTL ||
		value.DailyStaleMax <= value.DailyTTL ||
		value.AlertsTTL <= 0 || value.AlertsStaleMax <= value.AlertsTTL ||
		value.MaxLocations < 1 || value.MaxLocations > 1024 {
		return errors.New("weather cache policy is invalid")
	}
	return nil
}

func credentialsFromState(tokens []state.DeviceToken) ([]auth.Credential, error) {
	if len(tokens) == 0 || len(tokens) > maximumDeviceTokens {
		return nil, errors.New("1-32 device tokens are required")
	}
	credentials := make([]auth.Credential, 0, len(tokens))
	seenIDs := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if !identifierPattern.MatchString(token.ID) || len(strings.TrimSpace(token.Name)) < 1 ||
			len([]rune(token.Name)) > 64 {
			return nil, errors.New("device token identity is invalid")
		}
		if _, exists := seenIDs[token.ID]; exists {
			return nil, errors.New("device token IDs must be unique")
		}
		credential, ok := auth.CredentialFromHex(token.ID, token.Hash)
		if !ok {
			return nil, errors.New("device token hash is invalid")
		}
		seenIDs[token.ID] = struct{}{}
		credentials = append(credentials, credential)
	}
	return credentials, nil
}

func publicLocation(point location.Point) weather.PublicLocation {
	return weather.PublicLocation{
		City: point.City, Region: point.Region, Country: point.Country, Timezone: point.Timezone,
		Source: point.Source, Provider: point.Provider, Precision: point.Precision,
		LocationKey: point.Key,
	}
}

func deviceIDs(tokens []state.DeviceToken) []string {
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		result = append(result, token.ID)
	}
	return result
}

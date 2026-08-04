package weather

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

type fakeProvider struct {
	calls  atomic.Int32
	closes atomic.Int32
	ready  error
	fetch  func(context.Context, Kind, location.Point) (ProviderResult, error)
}

func (f *fakeProvider) Source() Source {
	return Source{ID: "qweather", Name: "QWeather", AttributionURL: "https://www.qweather.com/"}
}

func (f *fakeProvider) Fetch(ctx context.Context, kind Kind,
	point location.Point) (ProviderResult, error) {
	f.calls.Add(1)
	return f.fetch(ctx, kind, point)
}

func (f *fakeProvider) Ready() error { return f.ready }

func (f *fakeProvider) Diagnostics() ProviderDiagnostics {
	if f.ready != nil {
		return ProviderDiagnostics{Status: "blocked"}
	}
	return ProviderDiagnostics{Status: "ready"}
}

func (f *fakeProvider) Close() error {
	f.closes.Add(1)
	return nil
}

func TestServiceCachesAndIsolatesLocations(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{fetch: successfulFetch(now)}
	service := newTestService(provider, 1)
	service.now = func() time.Time { return now }
	defer closeService(t, service)

	pointA := location.Point{Latitude: 30.1, Longitude: 120.1, Source: "ip", Precision: "city"}
	pointB := location.Point{Latitude: 31.1, Longitude: 121.1, Source: "ip", Precision: "city"}
	if _, err := service.Get(context.Background(), KindCurrent, pointA); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), KindCurrent, pointA); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("fresh cache miss: %d calls", provider.calls.Load())
	}
	if _, err := service.Get(context.Background(), KindCurrent, pointB); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), KindCurrent, pointA); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 3 {
		t.Fatalf("expected LRU eviction, got %d calls", provider.calls.Load())
	}
}

func TestServiceCollapsesConcurrentFirstFetch(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &fakeProvider{fetch: func(ctx context.Context, _ Kind,
		_ location.Point) (ProviderResult, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		select {
		case <-ctx.Done():
			return ProviderResult{}, ctx.Err()
		case <-release:
			return ProviderResult{UpdatedAt: now, Data: Current{TemperatureC: 20}}, nil
		}
	}}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	defer closeService(t, service)

	const callers = 12
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, callers)
	for range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := service.Get(context.Background(), KindCurrent,
				location.Point{Latitude: 30, Longitude: 120})
			errorsChannel <- err
		}()
	}
	<-started
	close(release)
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("expected one collapsed call, got %d", provider.calls.Load())
	}
}

func TestServiceReturnsStaleAndRefreshesInBackground(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	provider := &fakeProvider{}
	provider.fetch = func(ctx context.Context, _ Kind,
		_ location.Point) (ProviderResult, error) {
		if provider.calls.Load() == 1 {
			return ProviderResult{UpdatedAt: now, Data: Current{TemperatureC: 20}}, nil
		}
		close(refreshStarted)
		select {
		case <-ctx.Done():
			return ProviderResult{}, ctx.Err()
		case <-releaseRefresh:
			return ProviderResult{UpdatedAt: now.Add(time.Hour), Data: Current{TemperatureC: 21}}, nil
		}
	}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	defer closeService(t, service)
	point := location.Point{Latitude: 30, Longitude: 120, Source: "ip", Precision: "city"}

	if _, err := service.Get(context.Background(), KindCurrent, point); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	stale, err := service.Get(context.Background(), KindCurrent, point)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || stale.Data.(Current).TemperatureC != 20 {
		t.Fatalf("unexpected stale envelope %#v", stale)
	}
	<-refreshStarted
	close(releaseRefresh)
	service.wg.Wait()
	fresh, err := service.Get(context.Background(), KindCurrent, point)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Stale || fresh.Data.(Current).TemperatureC != 21 || provider.calls.Load() != 2 {
		t.Fatalf("unexpected refreshed envelope %#v calls=%d", fresh, provider.calls.Load())
	}
}

func TestServiceRejectsOveragedDataAfterRefreshFailure(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{}
	provider.fetch = func(context.Context, Kind, location.Point) (ProviderResult, error) {
		if provider.calls.Load() == 1 {
			return ProviderResult{UpdatedAt: now, Data: Current{}}, nil
		}
		return ProviderResult{}, errors.New("offline")
	}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	defer closeService(t, service)
	point := location.Point{Latitude: 30, Longitude: 120}
	if _, err := service.Get(context.Background(), KindCurrent, point); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := service.Get(context.Background(), KindCurrent, point); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestServiceRejectsAlertsAtOneHourStaleLimit(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{}
	provider.fetch = func(context.Context, Kind, location.Point) (ProviderResult, error) {
		if provider.calls.Load() == 1 {
			return ProviderResult{UpdatedAt: now, Data: Alerts{Items: []Alert{}}}, nil
		}
		return ProviderResult{}, errors.New("offline")
	}
	service := NewService(provider, slog.New(slog.NewTextHandler(io.Discard, nil)), CacheOptions{
		CurrentTTL: time.Minute, CurrentStaleMax: 10 * time.Minute,
		HourlyTTL: time.Minute, HourlyStaleMax: 10 * time.Minute,
		DailyTTL: time.Minute, DailyStaleMax: 10 * time.Minute,
		AlertsTTL: 10 * time.Minute, AlertsStaleMax: time.Hour,
		MaxLocations: 64,
	})
	service.now = func() time.Time { return now }
	defer closeService(t, service)
	point := location.Point{Latitude: 30, Longitude: 120}
	if _, err := service.Get(context.Background(), KindAlerts, point); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := service.Get(context.Background(), KindAlerts, point); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected alerts to expire at the stale limit, got %v", err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("expected an upstream retry after alert expiry, got %d calls", provider.calls.Load())
	}
}

func TestServiceReadinessDelegatesProvider(t *testing.T) {
	provider := &fakeProvider{ready: errors.New("circuit open"), fetch: successfulFetch(time.Now())}
	service := newTestService(provider, 64)
	defer closeService(t, service)
	if service.Ready() == nil {
		t.Fatal("expected readiness error")
	}
}

func TestServiceDiagnosticsAreAggregatedWithoutSensitiveDimensions(t *testing.T) {
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{fetch: successfulFetch(now)}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	service.started = now.Add(-time.Minute)
	defer closeService(t, service)
	point := location.Point{Latitude: 30, Longitude: 120}
	if _, err := service.Get(context.Background(), KindCurrent, point); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), KindCurrent, point); err != nil {
		t.Fatal(err)
	}
	diagnostics := service.Diagnostics()
	current := diagnostics.Kinds[KindCurrent]
	if diagnostics.Provider.Status != "ready" || diagnostics.Locations != 1 ||
		diagnostics.Entries != 1 || diagnostics.LastSuccessAt == nil ||
		current.Requests != 2 || current.FreshHits != 1 || current.FetchSuccesses != 1 ||
		current.Entries != 1 || current.FreshEntries != 1 {
		t.Fatalf("unexpected diagnostics %#v", diagnostics)
	}
	if _, ok := diagnostics.Kinds[KindAlerts]; !ok {
		t.Fatalf("alerts diagnostics are missing: %#v", diagnostics.Kinds)
	}
}

func TestServiceUsesExponentialBackoffAndResetsAfterSuccess(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	shouldFail := true
	provider := &fakeProvider{fetch: func(context.Context, Kind, location.Point) (ProviderResult, error) {
		if shouldFail {
			return ProviderResult{}, errors.New("offline")
		}
		return ProviderResult{UpdatedAt: now, Data: Current{}}, nil
	}}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	service.jitter = func(delay time.Duration) time.Duration { return delay }
	defer closeService(t, service)
	point := location.Point{Latitude: 30, Longitude: 120}

	for failure, expected := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second} {
		if _, err := service.Get(context.Background(), KindCurrent, point); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("failure %d unexpectedly succeeded", failure+1)
		}
		entry := service.entries[cacheKey{location: point.CacheKey(), kind: KindCurrent}]
		if got := entry.retryAfter.Sub(now); got != expected {
			t.Fatalf("failure %d backoff = %s, want %s", failure+1, got, expected)
		}
		now = entry.retryAfter
	}

	shouldFail = false
	if _, err := service.Get(context.Background(), KindCurrent, point); err != nil {
		t.Fatal(err)
	}
	entry := service.entries[cacheKey{location: point.CacheKey(), kind: KindCurrent}]
	if entry.failures != 0 || !entry.retryAfter.IsZero() {
		t.Fatalf("successful refresh did not reset backoff: %#v", entry)
	}
}

type delayedError struct{ delay time.Duration }

func (e delayedError) Error() string             { return "delayed" }
func (e delayedError) RetryDelay() time.Duration { return e.delay }

func TestServiceHonorsProviderRetryDelay(t *testing.T) {
	if got := retryDelay(delayedError{delay: 15 * time.Minute}, 8,
		func(time.Duration) time.Duration { return time.Second }); got != 15*time.Minute {
		t.Fatalf("provider delay was changed: %s", got)
	}
}

func TestJitterDelayStaysWithinBounds(t *testing.T) {
	for range 100 {
		got := jitterDelay(100 * time.Second)
		if got < 80*time.Second || got > 120*time.Second {
			t.Fatalf("jitter out of bounds: %s", got)
		}
	}
	if got := jitterDelay(maximumRetryDelay); got > maximumRetryDelay {
		t.Fatalf("maximum retry delay exceeded: %s", got)
	}
}

func TestServiceClosesProviderOnce(t *testing.T) {
	provider := &fakeProvider{fetch: successfulFetch(time.Now())}
	service := newTestService(provider, 64)
	closeService(t, service)
	closeService(t, service)
	if provider.closes.Load() != 1 {
		t.Fatalf("provider closed %d times", provider.closes.Load())
	}
}

func newTestService(provider Provider, maximum int) *Service {
	return NewService(provider, slog.New(slog.NewTextHandler(io.Discard, nil)), CacheOptions{
		CurrentTTL:      time.Minute,
		CurrentStaleMax: 10 * time.Minute,
		HourlyTTL:       time.Minute,
		HourlyStaleMax:  10 * time.Minute,
		DailyTTL:        time.Minute,
		DailyStaleMax:   10 * time.Minute,
		AlertsTTL:       time.Minute,
		AlertsStaleMax:  10 * time.Minute,
		MaxLocations:    maximum,
	})
}

func successfulFetch(updatedAt time.Time) func(context.Context, Kind, location.Point) (ProviderResult, error) {
	return func(context.Context, Kind, location.Point) (ProviderResult, error) {
		return ProviderResult{UpdatedAt: updatedAt, Data: Current{TemperatureC: 20}}, nil
	}
}

func closeService(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

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
	calls atomic.Int32
	ready error
	fetch func(context.Context, Kind, location.Point) (ProviderResult, error)
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

func TestServiceReadinessDelegatesProvider(t *testing.T) {
	provider := &fakeProvider{ready: errors.New("circuit open"), fetch: successfulFetch(time.Now())}
	service := newTestService(provider, 64)
	defer closeService(t, service)
	if service.Ready() == nil {
		t.Fatal("expected readiness error")
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

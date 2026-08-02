package weather

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

// ErrUnavailable means no acceptable cached or upstream data is available.
var ErrUnavailable = errors.New("weather data unavailable")

type policy struct {
	freshFor time.Duration
	staleFor time.Duration
}

type cacheKey struct {
	location string
	kind     Kind
}

type cacheEntry struct {
	result     ProviderResult
	fetchedAt  time.Time
	validUntil time.Time
	staleUntil time.Time
	hasValue   bool
	refreshing bool
	wait       chan struct{}
	lastError  error
	retryAfter time.Time
}

type locationEntry struct {
	key string
}

// CacheOptions configures weather cache behavior.
type CacheOptions struct {
	CurrentTTL      time.Duration
	CurrentStaleMax time.Duration
	HourlyTTL       time.Duration
	HourlyStaleMax  time.Duration
	DailyTTL        time.Duration
	DailyStaleMax   time.Duration
	MaxLocations    int
}

// Service provides stale-while-revalidate caching around a provider.
type Service struct {
	provider Provider
	source   Source
	logger   *slog.Logger
	policies map[Kind]policy
	maximum  int
	now      func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	entries   map[cacheKey]*cacheEntry
	locations map[string]*list.Element
	lru       list.List
}

// NewService constructs an in-memory weather service.
func NewService(provider Provider, logger *slog.Logger, options CacheOptions) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		provider: provider,
		source:   provider.Source(),
		logger:   logger,
		policies: map[Kind]policy{
			KindCurrent: {freshFor: options.CurrentTTL, staleFor: options.CurrentStaleMax},
			KindHourly:  {freshFor: options.HourlyTTL, staleFor: options.HourlyStaleMax},
			KindDaily:   {freshFor: options.DailyTTL, staleFor: options.DailyStaleMax},
		},
		maximum:   options.MaxLocations,
		now:       time.Now,
		ctx:       ctx,
		cancel:    cancel,
		entries:   make(map[cacheKey]*cacheEntry),
		locations: make(map[string]*list.Element),
	}
}

// Get returns fresh data, stale data with background refresh, or a synchronous
// first fetch. Concurrent refreshes for the same key are collapsed.
func (s *Service) Get(ctx context.Context, kind Kind,
	point location.Point) (Envelope, error) {
	cachePolicy, ok := s.policies[kind]
	if !ok {
		return Envelope{}, fmt.Errorf("unsupported weather kind %q", kind)
	}
	key := cacheKey{location: point.CacheKey(), kind: kind}

	for {
		now := s.now().UTC()
		s.mu.Lock()
		s.touchLocationLocked(key.location)
		entry := s.entries[key]
		if entry == nil {
			entry = &cacheEntry{}
			s.entries[key] = entry
		}
		if entry.hasValue && now.Before(entry.validUntil) {
			envelope := envelopeFrom(entry, point, s.source, false)
			s.mu.Unlock()
			return envelope, nil
		}
		if entry.hasValue && now.Before(entry.staleUntil) {
			if !entry.refreshing && !now.Before(entry.retryAfter) {
				s.beginRefreshLocked(entry)
				s.wg.Add(1)
				go s.refreshAsync(key, entry, kind, point, cachePolicy)
			}
			envelope := envelopeFrom(entry, point, s.source, true)
			s.mu.Unlock()
			return envelope, nil
		}
		if !entry.refreshing && now.Before(entry.retryAfter) {
			err := entry.lastError
			if err == nil {
				err = ErrUnavailable
			}
			s.mu.Unlock()
			return Envelope{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if entry.refreshing {
			wait := entry.wait
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return Envelope{}, ctx.Err()
			case <-wait:
				continue
			}
		}

		s.beginRefreshLocked(entry)
		s.mu.Unlock()
		result, err := s.provider.Fetch(ctx, kind, point)
		s.finishRefresh(entry, result, cachePolicy, err)
		if err != nil {
			return Envelope{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
}

// Ready delegates provider configuration health without making an upstream call.
func (s *Service) Ready() error {
	return s.provider.Ready()
}

// Close cancels refresh workers and waits for them to stop.
func (s *Service) Close(ctx context.Context) error {
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Service) refreshAsync(key cacheKey, entry *cacheEntry, kind Kind,
	point location.Point, cachePolicy policy) {
	defer s.wg.Done()
	result, err := s.provider.Fetch(s.ctx, kind, point)
	if err != nil {
		s.logger.Warn("weather refresh failed", "kind", kind, "error", err)
	}
	s.finishRefresh(entry, result, cachePolicy, err)
}

func (s *Service) beginRefreshLocked(entry *cacheEntry) {
	entry.refreshing = true
	entry.wait = make(chan struct{})
}

func (s *Service) finishRefresh(entry *cacheEntry, result ProviderResult,
	cachePolicy policy, refreshError error) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if refreshError == nil {
		entry.result = result
		entry.fetchedAt = now
		entry.validUntil = now.Add(cachePolicy.freshFor)
		entry.staleUntil = now.Add(cachePolicy.staleFor)
		entry.hasValue = true
		entry.lastError = nil
		entry.retryAfter = time.Time{}
	} else {
		entry.lastError = refreshError
		entry.retryAfter = now.Add(retryDelay(refreshError))
	}
	entry.refreshing = false
	if entry.wait != nil {
		close(entry.wait)
		entry.wait = nil
	}
}

func (s *Service) touchLocationLocked(key string) {
	if element, ok := s.locations[key]; ok {
		s.lru.MoveToFront(element)
		return
	}
	element := s.lru.PushFront(locationEntry{key: key})
	s.locations[key] = element
	for len(s.locations) > s.maximum {
		oldest := s.lru.Back()
		if oldest == nil {
			return
		}
		oldKey := oldest.Value.(locationEntry).key
		delete(s.locations, oldKey)
		s.lru.Remove(oldest)
		for entryKey := range s.entries {
			if entryKey.location == oldKey {
				delete(s.entries, entryKey)
			}
		}
	}
}

func envelopeFrom(entry *cacheEntry, point location.Point, source Source, stale bool) Envelope {
	return Envelope{
		SchemaVersion: 1,
		Source:        source,
		Location: PublicLocation{
			City:      point.City,
			Region:    point.Region,
			Country:   point.Country,
			Timezone:  point.Timezone,
			Source:    point.Source,
			Provider:  point.Provider,
			Precision: point.Precision,
		},
		FetchedAt:  entry.fetchedAt,
		UpdatedAt:  entry.result.UpdatedAt,
		ValidUntil: entry.validUntil,
		Stale:      stale,
		Data:       entry.result.Data,
	}
}

type retryDelayProvider interface {
	RetryDelay() time.Duration
}

func retryDelay(err error) time.Duration {
	var provider retryDelayProvider
	if errors.As(err, &provider) {
		delay := provider.RetryDelay()
		if delay > 0 {
			return delay
		}
	}
	return 5 * time.Second
}

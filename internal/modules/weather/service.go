package weather

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

const (
	initialRetryDelay = 5 * time.Second
	maximumRetryDelay = 5 * time.Minute
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
	failures   uint
}

type kindCounters struct {
	requests       uint64
	freshHits      uint64
	staleHits      uint64
	fetchSuccesses uint64
	fetchFailures  uint64
}

// KindDiagnostics describes one process-local cache partition.
type KindDiagnostics struct {
	Entries        int    `json:"entries"`
	FreshEntries   int    `json:"fresh_entries"`
	StaleEntries   int    `json:"stale_entries"`
	Refreshing     int    `json:"refreshing"`
	Requests       uint64 `json:"requests"`
	FreshHits      uint64 `json:"fresh_hits"`
	StaleHits      uint64 `json:"stale_hits"`
	FetchSuccesses uint64 `json:"fetch_successes"`
	FetchFailures  uint64 `json:"fetch_failures"`
}

// Diagnostics is a privacy-preserving snapshot of one active weather runtime.
type Diagnostics struct {
	GeneratedAt    time.Time                `json:"generated_at"`
	RuntimeStarted time.Time                `json:"runtime_started_at"`
	Provider       ProviderDiagnostics      `json:"provider"`
	LastSuccessAt  *time.Time               `json:"last_success_at,omitempty"`
	LastErrorAt    *time.Time               `json:"last_error_at,omitempty"`
	LastErrorClass string                   `json:"last_error_class,omitempty"`
	Locations      int                      `json:"locations"`
	Entries        int                      `json:"entries"`
	Kinds          map[Kind]KindDiagnostics `json:"kinds"`
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
	AlertsTTL       time.Duration
	AlertsStaleMax  time.Duration
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
	jitter   func(time.Duration) time.Duration
	started  time.Time

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	close    sync.Once
	closeErr error

	mu             sync.Mutex
	entries        map[cacheKey]*cacheEntry
	locations      map[string]*list.Element
	lru            list.List
	counters       map[Kind]*kindCounters
	lastSuccessAt  time.Time
	lastErrorAt    time.Time
	lastErrorClass string
}

// NewService constructs an in-memory weather service.
func NewService(provider Provider, logger *slog.Logger, options CacheOptions) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now().UTC()
	return &Service{
		provider: provider,
		source:   provider.Source(),
		logger:   logger,
		policies: map[Kind]policy{
			KindCurrent: {freshFor: options.CurrentTTL, staleFor: options.CurrentStaleMax},
			KindHourly:  {freshFor: options.HourlyTTL, staleFor: options.HourlyStaleMax},
			KindDaily:   {freshFor: options.DailyTTL, staleFor: options.DailyStaleMax},
			KindAlerts:  {freshFor: options.AlertsTTL, staleFor: options.AlertsStaleMax},
		},
		maximum:   options.MaxLocations,
		now:       time.Now,
		jitter:    jitterDelay,
		started:   started,
		ctx:       ctx,
		cancel:    cancel,
		entries:   make(map[cacheKey]*cacheEntry),
		locations: make(map[string]*list.Element),
		counters: map[Kind]*kindCounters{
			KindCurrent: {}, KindHourly: {}, KindDaily: {}, KindAlerts: {},
		},
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
	s.mu.Lock()
	s.counters[kind].requests++
	s.mu.Unlock()

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
			s.counters[kind].freshHits++
			envelope := envelopeFrom(entry, point, s.source, false)
			s.mu.Unlock()
			return envelope, nil
		}
		if entry.hasValue && now.Before(entry.staleUntil) {
			s.counters[kind].staleHits++
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
		s.finishRefresh(entry, kind, result, cachePolicy, err)
		if err != nil {
			return Envelope{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		s.mu.Lock()
		envelope := envelopeFrom(entry, point, s.source, false)
		s.mu.Unlock()
		return envelope, nil
	}
}

// Ready delegates provider configuration health without making an upstream call.
func (s *Service) Ready() error {
	return s.provider.Ready()
}

// Diagnostics returns an in-memory snapshot without performing provider I/O.
func (s *Service) Diagnostics() Diagnostics {
	now := s.now().UTC()
	provider := s.provider.Diagnostics()
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := make(map[Kind]KindDiagnostics, len(s.counters))
	for kind, counters := range s.counters {
		kinds[kind] = KindDiagnostics{
			Requests: counters.requests, FreshHits: counters.freshHits,
			StaleHits: counters.staleHits, FetchSuccesses: counters.fetchSuccesses,
			FetchFailures: counters.fetchFailures,
		}
	}
	for key, entry := range s.entries {
		value := kinds[key.kind]
		value.Entries++
		if entry.hasValue && now.Before(entry.validUntil) {
			value.FreshEntries++
		} else if entry.hasValue && now.Before(entry.staleUntil) {
			value.StaleEntries++
		}
		if entry.refreshing {
			value.Refreshing++
		}
		kinds[key.kind] = value
	}
	result := Diagnostics{
		GeneratedAt: now, RuntimeStarted: s.started, Provider: provider,
		LastErrorClass: s.lastErrorClass, Locations: len(s.locations),
		Entries: len(s.entries), Kinds: kinds,
	}
	if !s.lastSuccessAt.IsZero() {
		value := s.lastSuccessAt
		result.LastSuccessAt = &value
	}
	if !s.lastErrorAt.IsZero() {
		value := s.lastErrorAt
		result.LastErrorAt = &value
	}
	return result
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
		return errors.Join(ctx.Err(), s.closeProvider())
	case <-done:
		return s.closeProvider()
	}
}

func (s *Service) closeProvider() error {
	s.close.Do(func() { s.closeErr = s.provider.Close() })
	return s.closeErr
}

func (s *Service) refreshAsync(key cacheKey, entry *cacheEntry, kind Kind,
	point location.Point, cachePolicy policy) {
	defer s.wg.Done()
	result, err := s.provider.Fetch(s.ctx, kind, point)
	if err != nil {
		s.logger.Warn("weather refresh failed", "kind", kind, "error", err)
	}
	s.finishRefresh(entry, kind, result, cachePolicy, err)
}

func (s *Service) beginRefreshLocked(entry *cacheEntry) {
	entry.refreshing = true
	entry.wait = make(chan struct{})
}

func (s *Service) finishRefresh(entry *cacheEntry, kind Kind, result ProviderResult,
	cachePolicy policy, refreshError error) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if refreshError == nil {
		s.counters[kind].fetchSuccesses++
		s.lastSuccessAt = now
		entry.result = result
		entry.fetchedAt = now
		entry.validUntil = now.Add(cachePolicy.freshFor)
		entry.staleUntil = now.Add(cachePolicy.staleFor)
		entry.hasValue = true
		entry.lastError = nil
		entry.retryAfter = time.Time{}
		entry.failures = 0
	} else {
		s.counters[kind].fetchFailures++
		s.lastErrorAt = now
		s.lastErrorClass = diagnosticErrorClass(refreshError)
		entry.lastError = refreshError
		entry.failures++
		entry.retryAfter = now.Add(retryDelay(refreshError, entry.failures, s.jitter))
	}
	entry.refreshing = false
	if entry.wait != nil {
		close(entry.wait)
		entry.wait = nil
	}
}

type diagnosticClasser interface {
	DiagnosticClass() string
}

func diagnosticErrorClass(err error) string {
	var classified diagnosticClasser
	if errors.As(err, &classified) {
		return classified.DiagnosticClass()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "unknown"
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
			City:        point.City,
			Region:      point.Region,
			Country:     point.Country,
			Timezone:    point.Timezone,
			Source:      point.Source,
			Provider:    point.Provider,
			Precision:   point.Precision,
			LocationKey: point.Key,
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

func retryDelay(err error, failures uint, jitter func(time.Duration) time.Duration) time.Duration {
	var provider retryDelayProvider
	if errors.As(err, &provider) {
		delay := provider.RetryDelay()
		if delay > 0 {
			return delay
		}
	}
	if failures == 0 {
		failures = 1
	}
	delay := initialRetryDelay
	for count := uint(1); count < failures && delay < maximumRetryDelay; count++ {
		delay *= 2
	}
	if delay > maximumRetryDelay {
		delay = maximumRetryDelay
	}
	return jitter(delay)
}

func jitterDelay(delay time.Duration) time.Duration {
	minimum := delay * 80 / 100
	span := delay * 40 / 100
	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return delay
	}
	result := minimum + time.Duration(binary.LittleEndian.Uint64(randomBytes[:])%uint64(span+1))
	if result > maximumRetryDelay {
		return maximumRetryDelay
	}
	return result
}

package location

import (
	"context"
	"math"
	"sync"
	"time"
)

// LocalizedMetadata contains display-safe localized display names. The
// country remains the ISO code supplied by the device or inference source.
type LocalizedMetadata struct {
	City     string
	Region   string
	Timezone string
}

// Localizer resolves localized display names for a normalized point.
// Implementations must not leak coordinates, upstream IDs, or raw response
// data in errors or logs, and must return validated values.
type Localizer interface {
	Localize(ctx context.Context, point Point) (LocalizedMetadata, error)
}

// ApplyLocalized overlays validated non-empty localized fields onto a copy
// of point and reports whether any display field actually changed. Invalid or
// empty values are skipped so callers always keep a display-safe result, and
// the boolean lets callers claim attribution only when names were overlaid.
func ApplyLocalized(point Point, metadata LocalizedMetadata) (Point, bool) {
	changed := false
	if value, ok := validatedLocalizedField(metadata.City); ok && value != point.City {
		point.City = value
		changed = true
	}
	if value, ok := validatedLocalizedField(metadata.Region); ok && value != point.Region {
		point.Region = value
		changed = true
	}
	if value, ok := validatedLocalizedField(metadata.Timezone); ok && value != point.Timezone {
		point.Timezone = value
		changed = true
	}
	return point, changed
}

func validatedLocalizedField(value string) (string, bool) {
	normalized, err := normalizeMetadata(value)
	if err != nil || normalized == "" {
		return "", false
	}
	return normalized, true
}

// LocalizationTimeout bounds a best-effort display localization attempt so a
// slow upstream lookup cannot stall a weather or location response.
const LocalizationTimeout = 3 * time.Second

type localizeBucket struct {
	tokens  float64
	updated time.Time
}

// LocalizeLimiter bounds best-effort display localization so one device
// cannot drive unbounded billable upstream lookup requests. Budget is a
// per-device token bucket; a global in-flight cap prevents localization
// from saturating upstream slots.
type LocalizeLimiter struct {
	capacity float64
	interval time.Duration
	now      func() time.Time
	slots    chan struct{}

	mu      sync.Mutex
	devices map[string]*localizeBucket
}

// NewLocalizeLimiter constructs a per-device token bucket with capacity
// tokens refilled at one per interval, plus concurrency in-flight slots.
// Capacity and concurrency must be at least 1 and interval strictly positive;
// violating callers panic rather than produce an unusable limiter.
func NewLocalizeLimiter(capacity int, interval time.Duration, concurrency int) *LocalizeLimiter {
	if capacity < 1 || concurrency < 1 || interval <= 0 {
		panic("location: invalid LocalizeLimiter configuration")
	}
	return &LocalizeLimiter{
		capacity: float64(capacity), interval: interval, now: time.Now,
		slots: make(chan struct{}, concurrency), devices: make(map[string]*localizeBucket),
	}
}

// Try returns a release function and true when a global slot is free and the
// device has budget. When true, the caller must call release exactly once.
// A slot rejection does not consume the device budget.
func (l *LocalizeLimiter) Try(deviceID string) (func(), bool) {
	select {
	case l.slots <- struct{}{}:
	default:
		return nil, false
	}
	var once sync.Once
	release := func() { once.Do(func() { <-l.slots }) }

	l.mu.Lock()
	now := l.now()
	bucket, exists := l.devices[deviceID]
	if !exists {
		bucket = &localizeBucket{tokens: l.capacity, updated: now}
		l.devices[deviceID] = bucket
	}
	if elapsed := now.Sub(bucket.updated); elapsed > 0 {
		bucket.tokens = math.Min(l.capacity, bucket.tokens+float64(elapsed)/float64(l.interval))
		bucket.updated = now
	}
	if bucket.tokens < 1 {
		l.mu.Unlock()
		<-l.slots
		return nil, false
	}
	bucket.tokens--
	l.mu.Unlock()
	return release, true
}

// Retain removes limiter state for device identities that no longer exist.
func (l *LocalizeLimiter) Retain(deviceIDs []string) {
	allowed := make(map[string]struct{}, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		allowed[deviceID] = struct{}{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for deviceID := range l.devices {
		if _, exists := allowed[deviceID]; !exists {
			delete(l.devices, deviceID)
		}
	}
}

// Package location validates privacy-reduced locations supplied by authenticated devices.
package location

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	gridScale            = 10.0
	maximumMetadataRunes = 128

	HeaderLatitude  = "X-MT-Location-Latitude"
	HeaderLongitude = "X-MT-Location-Longitude"
	HeaderProvider  = "X-MT-Location-Provider"
	HeaderCity      = "X-MT-Location-City"
	HeaderRegion    = "X-MT-Location-Region"
	HeaderCountry   = "X-MT-Location-Country"
	HeaderTimezone  = "X-MT-Location-Timezone"
)

var (
	// ErrRequired means no device location headers are present and no
	// IP-inference fallback is configured.
	ErrRequired = errors.New("device location headers are required")
	// ErrPartial means only some required device location headers are present.
	ErrPartial = errors.New("device location headers must be provided all together or not at all")
	// ErrInvalid means the device location headers contain invalid data.
	ErrInvalid = errors.New("device location headers are invalid")

	providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
)

// Point contains normalized coordinates and display-safe metadata.
type Point struct {
	Latitude  float64
	Longitude float64
	City      string
	Region    string
	Country   string
	Timezone  string
	Source    string
	Provider  string
	Precision string
	// Key is the opaque grid-scope identity derived from the normalized
	// 0.1-degree grid. It is empty until Normalize runs; callers treat an
	// empty value as "no key".
	Key string
}

// FromRequest parses the fixed device location header contract. It returns
// (point, true, nil) when all required headers are present and valid,
// (Point{}, false, nil) when none are present so the caller may fall back to
// IP inference, and an error when only some headers are present or any value
// is invalid.
func FromRequest(request *http.Request) (Point, bool, error) {
	latitude := strings.TrimSpace(request.Header.Get(HeaderLatitude))
	longitude := strings.TrimSpace(request.Header.Get(HeaderLongitude))
	provider := strings.TrimSpace(request.Header.Get(HeaderProvider))
	if latitude == "" && longitude == "" && provider == "" {
		return Point{}, false, nil
	}
	if latitude == "" || longitude == "" || provider == "" {
		return Point{}, false, ErrPartial
	}
	if !providerPattern.MatchString(provider) {
		return Point{}, false, ErrInvalid
	}
	parsedLatitude, err := parseCoordinate(latitude, -90, 90)
	if err != nil {
		return Point{}, false, ErrInvalid
	}
	parsedLongitude, err := parseCoordinate(longitude, -180, 180)
	if err != nil {
		return Point{}, false, ErrInvalid
	}
	point, err := Normalize(Point{
		Latitude:  parsedLatitude,
		Longitude: parsedLongitude,
		City:      request.Header.Get(HeaderCity),
		Region:    request.Header.Get(HeaderRegion),
		Country:   request.Header.Get(HeaderCountry),
		Timezone:  request.Header.Get(HeaderTimezone),
		Source:    "device",
		Provider:  provider,
		Precision: "city",
	})
	if err != nil {
		return Point{}, false, ErrInvalid
	}
	return point, true, nil
}

// CacheKey returns a privacy-reduced location key.
func (p Point) CacheKey() string {
	return gridString(p.Latitude, p.Longitude)
}

// gridString is the canonical one-decimal representation of a grid point.
func gridString(latitude, longitude float64) string {
	return strconv.FormatFloat(latitude, 'f', 1, 64) + "," +
		strconv.FormatFloat(longitude, 'f', 1, 64)
}

// locationKey derives the opaque 16-hex grid-scope identity. It is not
// cryptographic: the 0.1-degree grid space is enumerable, so the value must
// not be presented as anonymous. It never contains coordinates directly.
func locationKey(latitude, longitude float64) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(gridString(latitude, longitude)))
	return fmt.Sprintf("%016x", hasher.Sum64())
}

// Normalize validates and rounds a point to the privacy grid.
func Normalize(point Point) (Point, error) {
	if math.IsNaN(point.Latitude) || math.IsInf(point.Latitude, 0) ||
		point.Latitude < -90 || point.Latitude > 90 ||
		math.IsNaN(point.Longitude) || math.IsInf(point.Longitude, 0) ||
		point.Longitude < -180 || point.Longitude > 180 {
		return Point{}, ErrInvalid
	}
	for _, field := range []*string{&point.City, &point.Region, &point.Country, &point.Timezone} {
		value, err := normalizeMetadata(*field)
		if err != nil {
			return Point{}, ErrInvalid
		}
		*field = value
	}
	point.Latitude = math.Round(point.Latitude*gridScale) / gridScale
	point.Longitude = math.Round(point.Longitude*gridScale) / gridScale
	if point.Latitude == 0 {
		point.Latitude = 0
	}
	if point.Longitude == 0 {
		point.Longitude = 0
	}
	point.Key = locationKey(point.Latitude, point.Longitude)
	return point, nil
}

func normalizeMetadata(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || len([]rune(value)) > maximumMetadataRunes {
		return "", ErrInvalid
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrInvalid
		}
	}
	return value, nil
}

func parseCoordinate(value string, minimum, maximum float64) (float64, error) {
	if len(value) > 32 {
		return 0, ErrInvalid
	}
	coordinate, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(coordinate) || math.IsInf(coordinate, 0) ||
		coordinate < minimum || coordinate > maximum {
		return 0, ErrInvalid
	}
	return coordinate, nil
}

type deviceBucket struct {
	tokens  float64
	updated time.Time
	grid    string
}

// ChangeLimiter limits how quickly one authenticated device can switch grids.
type ChangeLimiter struct {
	mu       sync.Mutex
	capacity float64
	interval time.Duration
	now      func() time.Time
	devices  map[string]*deviceBucket
}

// NewChangeLimiter constructs an in-memory per-device token bucket.
func NewChangeLimiter(capacity int, interval time.Duration) *ChangeLimiter {
	return &ChangeLimiter{
		capacity: float64(capacity), interval: interval, now: time.Now,
		devices: make(map[string]*deviceBucket),
	}
}

// Allow reports whether a device may use the requested grid and the retry delay.
func (l *ChangeLimiter) Allow(deviceID, grid string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bucket, exists := l.devices[deviceID]
	if !exists {
		l.devices[deviceID] = &deviceBucket{tokens: l.capacity, updated: now, grid: grid}
		return true, 0
	}
	if bucket.grid == grid {
		return true, 0
	}
	elapsed := now.Sub(bucket.updated)
	if elapsed > 0 {
		bucket.tokens = math.Min(l.capacity, bucket.tokens+float64(elapsed)/float64(l.interval))
		bucket.updated = now
	}
	if bucket.tokens < 1 {
		missing := 1 - bucket.tokens
		return false, time.Duration(math.Ceil(missing * float64(l.interval)))
	}
	bucket.tokens--
	bucket.grid = grid
	return true, 0
}

// Retain removes limiter state for revoked device identities.
func (l *ChangeLimiter) Retain(deviceIDs []string) {
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

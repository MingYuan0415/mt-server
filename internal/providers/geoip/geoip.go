// Package geoip resolves display-safe locations from a local GeoLite2 City
// MMDB file. Raw MaxMind records never leave this package.
package geoip

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"

	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

const (
	// Provider identifies the location data source in responses.
	Provider = "maxmind"
	// reloadInterval is how often the MMDB file metadata is checked for
	// replacement by an external updater.
	reloadInterval = 5 * time.Minute
	// closeWaitTimeout bounds how long Close waits for a poll that is stuck
	// in file I/O.
	closeWaitTimeout = 2 * time.Second
)

var (
	// ErrNotPublic means the source IP cannot be geolocated.
	ErrNotPublic = errors.New("source IP is not a public address")
	// ErrUnavailable means the database file is not loaded.
	ErrUnavailable = errors.New("geoip database is unavailable")
	// ErrNotFound means the database has no record for the IP.
	ErrNotFound = errors.New("IP has no geoip record")
)

// reservedPrefixes covers IANA special-purpose ranges that are not
// geolocatable even though netip classifies them as global unicast.
var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}

type fileInfo struct {
	size    int64
	modTime time.Time
}

// Store reads a GeoLite2 City database and hot-reloads it when the file is
// atomically replaced by an external updater. It is safe for concurrent use.
type Store struct {
	path   string
	logger *slog.Logger

	mu      sync.RWMutex
	reader  *geoip2.Reader
	loaded  os.FileInfo
	polling bool
	closed  bool

	openReader func(string) (*geoip2.Reader, error)
	interval   time.Duration

	closeOnce sync.Once
	done      chan struct{}
	stopped   chan struct{}
}

// New constructs a Store without opening the database. Use Start to load.
func New(path string, logger *slog.Logger) *Store {
	return newStore(path, logger, geoip2.Open)
}

// newStore constructs a Store with injectable opener and poll interval for
// tests. The opener must not change after construction.
func newStore(path string, logger *slog.Logger,
	openReader func(string) (*geoip2.Reader, error)) *Store {
	return newStoreWithInterval(path, logger, openReader, reloadInterval)
}

func newStoreWithInterval(path string, logger *slog.Logger,
	openReader func(string) (*geoip2.Reader, error), interval time.Duration) *Store {
	return &Store{
		path:       path,
		logger:     logger,
		openReader: openReader,
		interval:   interval,
		done:       make(chan struct{}),
		stopped:    make(chan struct{}),
	}
}

// Start attempts the initial load and begins polling for file replacement.
// Calling Start after Close has no effect.
func (s *Store) Start() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.reloadIfChanged()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.polling {
		return
	}
	s.polling = true
	go s.poll()
}

// Close stops polling and closes the loaded reader. It is safe to call before
// Start or multiple times. Callers must ensure no Resolve calls are in flight.
func (s *Store) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		polling := s.polling
		reader := s.reader
		s.reader = nil
		s.mu.Unlock()
		if polling {
			close(s.done)
			select {
			case <-s.stopped:
			case <-time.After(closeWaitTimeout):
				s.logger.Warn("geoip poll did not stop within the close timeout")
			}
		}
		if reader != nil {
			closeErr = reader.Close()
		}
	})
	return closeErr
}

func (s *Store) poll() {
	defer close(s.stopped)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.reloadIfChanged()
			select {
			case <-s.done:
				return
			default:
			}
		}
	}
}

// reloadIfChanged reopens the database when the file was replaced.
func (s *Store) reloadIfChanged() {
	current, err := os.Stat(s.path)
	if err != nil {
		return
	}
	s.mu.RLock()
	changed := s.reader == nil || !os.SameFile(current, s.loaded) ||
		s.loaded.Size() != current.Size() || !s.loaded.ModTime().Equal(current.ModTime())
	s.mu.RUnlock()
	if !changed {
		return
	}
	reader, err := s.openReader(s.path)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		s.logger.Warn("open geoip database failed", "error", err)
		return
	}
	if !isCityDatabase(reader.Metadata().DatabaseType) {
		s.logger.Warn("geoip database type is not a City database, keeping previous database",
			"database_type", reader.Metadata().DatabaseType)
		_ = reader.Close()
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = reader.Close()
		return
	}
	previous := s.reader
	s.reader = reader
	s.loaded = current
	s.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
}

// isCityDatabase reports whether the MMDB database type supports City records.
func isCityDatabase(databaseType string) bool {
	return strings.Contains(databaseType, "City")
}

// Resolve returns a normalized, display-safe location for a public IP.
func (s *Store) Resolve(ip netip.Addr) (location.Resolved, error) {
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if !isPublic(ip) {
		return location.Resolved{}, ErrNotPublic
	}
	s.mu.RLock()
	reader := s.reader
	if reader == nil {
		s.mu.RUnlock()
		return location.Resolved{}, ErrUnavailable
	}
	record, err := reader.City(net.IP(ip.AsSlice()))
	s.mu.RUnlock()
	if err != nil {
		return location.Resolved{}, ErrNotFound
	}
	if record.Location.Latitude == 0 && record.Location.Longitude == 0 {
		return location.Resolved{}, ErrNotFound
	}
	if record.Country.IsoCode == "" && len(record.City.Names) == 0 {
		return location.Resolved{}, ErrNotFound
	}
	region := ""
	if len(record.Subdivisions) > 0 {
		region = preferredName(record.Subdivisions[0].Names)
	}
	point := location.Point{
		Latitude:  record.Location.Latitude,
		Longitude: record.Location.Longitude,
		City:      preferredName(record.City.Names),
		Region:    region,
		Country:   record.Country.IsoCode,
		Timezone:  record.Location.TimeZone,
		Source:    "ip",
		Provider:  Provider,
		Precision: "coarse",
	}
	normalized, err := location.Normalize(point)
	if err != nil {
		return location.Resolved{}, fmt.Errorf("normalize geoip location: %w", err)
	}
	var accuracy *int
	if record.Location.AccuracyRadius > 0 {
		value := int(record.Location.AccuracyRadius)
		accuracy = &value
	}
	return location.Resolved{Point: normalized, AccuracyKm: accuracy}, nil
}

func preferredName(names map[string]string) string {
	for _, language := range []string{"zh-CN", "en", "de", "es", "fr", "ja", "pt-BR", "ru"} {
		if name := names[language]; name != "" {
			return name
		}
	}
	return ""
}

// isPublic reports whether ip is a geolocatable global unicast address.
func isPublic(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		!ip.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

package location

import (
	"errors"
	"net/http"
	"net/netip"
)

// ErrLocationUnavailable means neither explicit device headers nor IP
// inference produced a usable location.
var ErrLocationUnavailable = errors.New("device location is unavailable")

// Resolved is a display-safe IP-inferred location.
type Resolved struct {
	Point      Point
	AccuracyKm *int
}

// Resolver infers a display-safe location from a public source IP.
type Resolver interface {
	// Resolve returns a normalized, display-safe location for a public IP.
	Resolve(ip netip.Addr) (Resolved, error)
}

// Source resolves the effective request location, preferring explicit device
// headers, then trusted Cloudflare visitor-location headers, and finally the
// configured IP resolver.
type Source struct {
	extractor  *IPExtractor
	resolver   Resolver
	cloudflare *Cloudflare
}

// NewSource constructs a location source with IP inference only. resolver may
// be nil to disable IP inference; extractor is ignored when resolver is nil.
func NewSource(extractor *IPExtractor, resolver Resolver) *Source {
	return NewSourceWithCloudflare(extractor, resolver, nil)
}

// NewSourceWithCloudflare constructs a location source that also accepts
// trusted Cloudflare visitor-location headers before falling back to the IP
// resolver.
func NewSourceWithCloudflare(extractor *IPExtractor, resolver Resolver, cloudflare *Cloudflare) *Source {
	return &Source{extractor: extractor, resolver: resolver, cloudflare: cloudflare}
}

// Enabled reports whether any IP inference is configured.
func (s *Source) Enabled() bool {
	return s != nil && (s.resolver != nil || s.cloudflare != nil)
}

// EffectivePoint resolves the effective point for an authenticated request.
// Explicit headers win when complete. Partial or invalid headers return
// ErrPartial or ErrInvalid. Missing headers fall back to trusted Cloudflare
// visitor-location headers, then to IP inference when configured, returning
// ErrLocationUnavailable when inference fails and ErrRequired when no
// inference is configured. A malformed Cloudflare bundle fails closed with
// ErrLocationUnavailable and never downgrades to the IP resolver.
func (s *Source) EffectivePoint(r *http.Request) (Point, Resolved, error) {
	point, explicit, err := FromRequest(r)
	if err != nil {
		return Point{}, Resolved{}, err
	}
	if explicit {
		return point, Resolved{}, nil
	}
	if s == nil || (s.resolver == nil && s.cloudflare == nil) {
		return Point{}, Resolved{}, ErrRequired
	}
	if s.cloudflare != nil {
		cloudflarePoint, present, err := s.cloudflare.FromRequest(r)
		if err != nil {
			return Point{}, Resolved{}, ErrLocationUnavailable
		}
		if present {
			return cloudflarePoint, Resolved{}, nil
		}
	}
	if s.resolver == nil {
		return Point{}, Resolved{}, ErrLocationUnavailable
	}
	if s.extractor == nil {
		return Point{}, Resolved{}, ErrLocationUnavailable
	}
	ip, err := s.extractor.FromRequest(r)
	if err != nil {
		return Point{}, Resolved{}, ErrLocationUnavailable
	}
	resolved, err := s.resolver.Resolve(ip)
	if err != nil {
		return Point{}, Resolved{}, ErrLocationUnavailable
	}
	normalized, err := Normalize(resolved.Point)
	if err != nil {
		return Point{}, Resolved{}, ErrLocationUnavailable
	}
	resolved.Point = normalized
	return resolved.Point, resolved, nil
}

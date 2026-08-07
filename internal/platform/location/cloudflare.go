package location

import (
	"net/http"
	"net/netip"
	"strings"
)

const (
	// CloudflareHeaderLatitude is the visitor latitude added by Cloudflare's
	// "Add visitor location headers" managed transform.
	CloudflareHeaderLatitude = "CF-IPLatitude"
	// CloudflareHeaderLongitude is the visitor longitude added by Cloudflare's
	// "Add visitor location headers" managed transform.
	CloudflareHeaderLongitude = "CF-IPLongitude"
	// CloudflareHeaderCity is the optional visitor city display metadata.
	CloudflareHeaderCity = "CF-IPCity"
	// CloudflareHeaderRegion is the optional visitor region display metadata.
	CloudflareHeaderRegion = "CF-Region"
	// CloudflareHeaderCountry is the optional visitor country display metadata.
	CloudflareHeaderCountry = "CF-IPCountry"
	// CloudflareHeaderTimezone is the optional visitor timezone display metadata.
	CloudflareHeaderTimezone = "CF-Timezone"
)

// Cloudflare resolves locations from Cloudflare visitor-location headers that
// are honored only from direct peers in the configured trusted networks. The
// coordinate pair activates the source; a country-only header (for example
// the standalone IP Geolocation setting) never does.
type Cloudflare struct {
	nets []netip.Prefix
}

// NewCloudflare constructs a header trust boundary for Cloudflare
// visitor-location headers.
func NewCloudflare(nets []netip.Prefix) *Cloudflare {
	return &Cloudflare{nets: append([]netip.Prefix(nil), nets...)}
}

// FromRequest returns (point, true, nil) when a trusted peer supplied a
// complete, valid coordinate pair. A request without the pair, or from an
// untrusted peer, returns (Point{}, false, nil) so the caller may fall back.
// A malformed or duplicate pair from a trusted peer returns an error so the
// caller fails closed instead of silently switching sources.
func (c *Cloudflare) FromRequest(r *http.Request) (Point, bool, error) {
	peer, err := remoteAddr(r.RemoteAddr)
	if err != nil || !c.trusts(peer) {
		return Point{}, false, nil
	}
	latitude, hasLatitude, err := cloudflareHeader(r, CloudflareHeaderLatitude)
	if err != nil {
		return Point{}, false, ErrInvalid
	}
	longitude, hasLongitude, err := cloudflareHeader(r, CloudflareHeaderLongitude)
	if err != nil {
		return Point{}, false, ErrInvalid
	}
	if !hasLatitude && !hasLongitude {
		return Point{}, false, nil
	}
	if !hasLatitude || !hasLongitude {
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
	point := Point{
		Latitude:  parsedLatitude,
		Longitude: parsedLongitude,
		Source:    "ip",
		Provider:  "cloudflare",
		Precision: "coarse",
	}
	metadata := []struct {
		header string
		target *string
	}{
		{CloudflareHeaderCity, &point.City},
		{CloudflareHeaderRegion, &point.Region},
		{CloudflareHeaderCountry, &point.Country},
		{CloudflareHeaderTimezone, &point.Timezone},
	}
	for _, field := range metadata {
		value, present, err := cloudflareHeader(r, field.header)
		if err != nil {
			return Point{}, false, ErrInvalid
		}
		if present {
			normalized, err := normalizeMetadata(value)
			if err != nil {
				return Point{}, false, ErrInvalid
			}
			*field.target = normalized
		}
	}
	point, err = Normalize(point)
	if err != nil {
		return Point{}, false, ErrInvalid
	}
	return point, true, nil
}

// trusts reports whether the direct peer may supply Cloudflare location
// headers.
func (c *Cloudflare) trusts(peer netip.Addr) bool {
	for _, prefix := range c.nets {
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}

// cloudflareHeader returns a single non-empty value, distinguishing an
// absent header from duplicate or blank values.
func cloudflareHeader(r *http.Request, name string) (string, bool, error) {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", false, ErrInvalid
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", false, ErrInvalid
	}
	return value, true, nil
}

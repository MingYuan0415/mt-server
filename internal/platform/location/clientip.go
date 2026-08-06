package location

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ErrNoClientIP means the request has no usable client source address.
var ErrNoClientIP = errors.New("request has no usable client IP")

// IPExtractor resolves the client source IP through an explicit trusted-proxy
// contract. Forwarded client-IP headers are honored only when the direct peer
// belongs to a configured trusted network; otherwise the peer address is used
// so unauthenticated header spoofing cannot influence the result.
type IPExtractor struct {
	header string
	nets   []netip.Prefix
}

// NewIPExtractor constructs an extractor that honors header from peers in
// trustedNets. An empty header disables forwarded-header trust entirely.
func NewIPExtractor(header string, trustedNets []netip.Prefix) *IPExtractor {
	return &IPExtractor{
		header: strings.TrimSpace(header),
		nets:   append([]netip.Prefix(nil), trustedNets...),
	}
}

// FromRequest returns the request's source IP. The value is never logged or
// echoed back to clients.
func (e *IPExtractor) FromRequest(r *http.Request) (netip.Addr, error) {
	peer, err := remoteAddr(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	if e.header != "" && e.trusts(peer) {
		values := r.Header.Values(e.header)
		if len(values) == 1 {
			value := strings.TrimSpace(values[0])
			if value != "" && !strings.ContainsAny(value, ", \t") {
				if address, parseErr := netip.ParseAddr(value); parseErr == nil && address.Zone() == "" {
					return address.Unmap(), nil
				}
			}
		}
		return netip.Addr{}, ErrNoClientIP
	}
	return peer, nil
}

// trusts reports whether the direct peer may supply the client-IP header.
func (e *IPExtractor) trusts(peer netip.Addr) bool {
	for _, prefix := range e.nets {
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}

// remoteAddr parses the standard RemoteAddr, tolerating bare addresses.
func remoteAddr(value string) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		address, parseErr := netip.ParseAddr(strings.Trim(value, "[]"))
		if parseErr != nil {
			return netip.Addr{}, ErrNoClientIP
		}
		return address.Unmap(), nil
	}
	address, parseErr := netip.ParseAddr(strings.Trim(host, "[]"))
	if parseErr != nil {
		return netip.Addr{}, ErrNoClientIP
	}
	return address.Unmap(), nil
}

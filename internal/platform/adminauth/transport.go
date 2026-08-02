package adminauth

import (
	"net/http"
	"net/url"
	"strings"
)

// TransportPolicy enforces management transport and same-origin rules.
type TransportPolicy struct {
	allowInsecure    bool
	behindHTTPSProxy bool
}

// NewTransportPolicy constructs a management transport policy.
func NewTransportPolicy(allowInsecure, behindHTTPSProxy bool) *TransportPolicy {
	return &TransportPolicy{allowInsecure: allowInsecure, behindHTTPSProxy: behindHTTPSProxy}
}

// Secure reports whether the browser-facing request used HTTPS.
func (p *TransportPolicy) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return p.behindHTTPSProxy
}

// AllowWrite reports whether management secrets may be submitted.
func (p *TransportPolicy) AllowWrite(r *http.Request) bool {
	return p.allowInsecure || p.Secure(r)
}

// SameOrigin validates a browser Origin header against the effective request.
func (p *TransportPolicy) SameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	expectedScheme := "http"
	if p.Secure(r) {
		expectedScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, expectedScheme)
}

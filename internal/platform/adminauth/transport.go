package adminauth

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// TransportPolicy enforces management transport and same-origin rules.
type TransportPolicy struct {
	allowInsecure    bool
	behindHTTPSProxy bool
	publicOrigins    map[string]struct{}
}

// NewTransportPolicy constructs a management transport policy.
func NewTransportPolicy(allowInsecure, behindHTTPSProxy bool, publicOrigins ...string) *TransportPolicy {
	origins := make(map[string]struct{}, len(publicOrigins))
	for _, origin := range publicOrigins {
		origins[origin] = struct{}{}
	}
	return &TransportPolicy{
		allowInsecure: allowInsecure, behindHTTPSProxy: behindHTTPSProxy, publicOrigins: origins,
	}
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
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	origin, ok := canonicalOrigin(strings.TrimSpace(values[0]))
	if !ok {
		return false
	}
	if len(p.publicOrigins) > 0 {
		_, allowed := p.publicOrigins[origin]
		return allowed
	}
	expectedScheme := "http"
	if p.Secure(r) {
		expectedScheme = "https"
	}
	expected, ok := canonicalOrigin(expectedScheme + "://" + r.Host)
	return ok && origin == expected
}

func canonicalOrigin(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		strings.HasSuffix(parsed.Host, ":") {
		return "", false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.ContainsAny(hostname, " \t\r\n") {
		return "", false
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", false
		}
	}
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return parsed.Scheme + "://" + host, true
}

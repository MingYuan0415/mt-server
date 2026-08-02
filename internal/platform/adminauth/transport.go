package adminauth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
)

const MaximumPublicOrigins = 16

type originSnapshot struct {
	values map[string]struct{}
}

// TransportPolicy enforces management transport and same-origin rules.
type TransportPolicy struct {
	allowInsecure    bool
	behindHTTPSProxy bool
	publicOrigins    atomic.Pointer[originSnapshot]
}

// NewTransportPolicy constructs a management transport policy.
func NewTransportPolicy(allowInsecure, behindHTTPSProxy bool) *TransportPolicy {
	policy := &TransportPolicy{allowInsecure: allowInsecure, behindHTTPSProxy: behindHTTPSProxy}
	policy.ReplacePublicOrigins(nil)
	return policy
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
	origin, ok := RequestOrigin(r)
	if !ok {
		return false
	}
	if p.behindHTTPSProxy {
		_, allowed := p.publicOrigins.Load().values[origin]
		return allowed
	}
	expectedScheme := "http"
	if p.Secure(r) {
		expectedScheme = "https"
	}
	expected, ok := canonicalOrigin(expectedScheme + "://" + r.Host)
	return ok && origin == expected
}

// BehindHTTPSProxy reports whether the source is isolated behind trusted HTTPS termination.
func (p *TransportPolicy) BehindHTTPSProxy() bool { return p.behindHTTPSProxy }

// OriginMode returns the stable management origin-policy identifier.
func (p *TransportPolicy) OriginMode() string {
	if p.behindHTTPSProxy {
		return "proxy_allowlist"
	}
	return "direct_same_origin"
}

// ReplacePublicOrigins atomically installs an already-normalized allowlist.
func (p *TransportPolicy) ReplacePublicOrigins(origins []string) {
	values := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		values[origin] = struct{}{}
	}
	p.publicOrigins.Store(&originSnapshot{values: values})
}

// SetupOriginAllowed validates an initialization request against its candidate state.
func (p *TransportPolicy) SetupOriginAllowed(r *http.Request, origins []string) bool {
	if !p.behindHTTPSProxy {
		return p.SameOrigin(r)
	}
	origin, ok := RequestOrigin(r)
	if !ok {
		return false
	}
	for _, candidate := range origins {
		if candidate == origin {
			return true
		}
	}
	return false
}

// ValidatePublicOrigins canonicalizes stored origins for the active transport mode.
func (p *TransportPolicy) ValidatePublicOrigins(origins []string) ([]string, error) {
	normalized, err := NormalizePublicOrigins(origins)
	if err != nil {
		return nil, err
	}
	if p.behindHTTPSProxy && len(normalized) == 0 {
		return nil, errors.New("at least one HTTPS origin is required in proxy mode")
	}
	return normalized, nil
}

// RequestOrigin returns the single canonical browser Origin header.
func RequestOrigin(r *http.Request) (string, bool) {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return "", false
	}
	return canonicalOrigin(strings.TrimSpace(values[0]))
}

// NormalizePublicOrigins validates and canonicalizes a persistent HTTPS allowlist.
func NormalizePublicOrigins(origins []string) ([]string, error) {
	if len(origins) > MaximumPublicOrigins {
		return nil, fmt.Errorf("at most %d origins are allowed", MaximumPublicOrigins)
	}
	result := make([]string, 0, len(origins))
	seen := make(map[string]struct{}, len(origins))
	for _, value := range origins {
		origin, err := NormalizePublicOrigin(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[origin]; exists {
			return nil, errors.New("duplicate origin")
		}
		seen[origin] = struct{}{}
		result = append(result, origin)
	}
	return result, nil
}

// NormalizePublicOrigin validates one complete HTTPS origin.
func NormalizePublicOrigin(value string) (string, error) {
	origin, ok := canonicalOrigin(strings.TrimSpace(value))
	if !ok || !strings.HasPrefix(origin, "https://") {
		return "", errors.New("invalid HTTPS origin")
	}
	hostname := strings.TrimPrefix(origin, "https://")
	if host, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = host
	} else {
		hostname = strings.Trim(hostname, "[]")
	}
	if !validPublicHostname(hostname) {
		return "", errors.New("invalid HTTPS origin")
	}
	return origin, nil
}

// PublicOriginID returns the stable opaque identifier used by management routes.
func PublicOriginID(origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return base64.RawURLEncoding.EncodeToString(digest[:])
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
	portNumber := 0
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", false
		}
		portNumber = value
	}
	if (parsed.Scheme == "https" && portNumber == 443) ||
		(parsed.Scheme == "http" && portNumber == 80) {
		port = ""
	} else if port != "" {
		port = strconv.Itoa(portNumber)
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

func validPublicHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 || strings.ContainsAny(hostname, " \t\r\n") {
		return false
	}
	if net.ParseIP(hostname) != nil {
		return true
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

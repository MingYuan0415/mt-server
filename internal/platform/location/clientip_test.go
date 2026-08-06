package location

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIPExtractorTrustsHeaderOnlyFromTrustedPeers(t *testing.T) {
	extractor := NewIPExtractor("CF-Connecting-IP",
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})

	trusted := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	trusted.RemoteAddr = "10.1.2.3:54321"
	trusted.Header.Set("CF-Connecting-IP", "203.0.113.9")
	ip, err := extractor.FromRequest(trusted)
	if err != nil || ip != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("unexpected trusted result %v %v", ip, err)
	}

	untrusted := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	untrusted.RemoteAddr = "192.0.2.44:1234"
	untrusted.Header.Set("CF-Connecting-IP", "203.0.113.9")
	ip, err = extractor.FromRequest(untrusted)
	if err != nil || ip != netip.MustParseAddr("192.0.2.44") {
		t.Fatalf("forged header changed the result %v %v", ip, err)
	}
}

func TestIPExtractorRejectsMalformedTrustedHeader(t *testing.T) {
	extractor := NewIPExtractor("CF-Connecting-IP",
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	for _, value := range []string{"", "not-an-ip", "203.0.113.9, 198.51.100.7", "203.0.113.9 1.1.1.1"} {
		request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
		request.RemoteAddr = "10.1.2.3:54321"
		request.Header.Set("CF-Connecting-IP", value)
		if _, err := extractor.FromRequest(request); !errors.Is(err, ErrNoClientIP) {
			t.Errorf("header %q: expected ErrNoClientIP, got %v", value, err)
		}
	}
}

func TestIPExtractorRejectsDuplicateTrustedHeader(t *testing.T) {
	extractor := NewIPExtractor("CF-Connecting-IP",
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	request.Header.Add("CF-Connecting-IP", "203.0.113.9")
	request.Header.Add("CF-Connecting-IP", "198.51.100.7")
	if _, err := extractor.FromRequest(request); !errors.Is(err, ErrNoClientIP) {
		t.Fatalf("duplicate header must be rejected, got %v", err)
	}
}

func TestIPExtractorRejectsIPv6ZoneInTrustedHeader(t *testing.T) {
	extractor := NewIPExtractor("CF-Connecting-IP",
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	request.Header.Set("CF-Connecting-IP", "fe80::1%eth0")
	if _, err := extractor.FromRequest(request); !errors.Is(err, ErrNoClientIP) {
		t.Fatalf("zoned address must be rejected, got %v", err)
	}
}

func TestIPExtractorHandlesBareAndIPv6RemoteAddr(t *testing.T) {
	extractor := NewIPExtractor("", nil)

	bare := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	bare.RemoteAddr = "203.0.113.5"
	ip, err := extractor.FromRequest(bare)
	if err != nil || ip != netip.MustParseAddr("203.0.113.5") {
		t.Fatalf("unexpected bare result %v %v", ip, err)
	}

	v6 := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	v6.RemoteAddr = "[2001:db8::5]:8080"
	ip, err = extractor.FromRequest(v6)
	if err != nil || ip != netip.MustParseAddr("2001:db8::5") {
		t.Fatalf("unexpected IPv6 result %v %v", ip, err)
	}

	invalid := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	invalid.RemoteAddr = "garbage"
	if _, err := extractor.FromRequest(invalid); !errors.Is(err, ErrNoClientIP) {
		t.Fatalf("expected ErrNoClientIP, got %v", err)
	}
}

package location

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

type fakeResolver struct {
	resolved Resolved
	err      error
}

func (f *fakeResolver) Resolve(ip netip.Addr) (Resolved, error) {
	return f.resolved, f.err
}

func TestSourcePrefersExplicitHeaders(t *testing.T) {
	source := NewSource(NewIPExtractor("", nil), &fakeResolver{
		resolved: Resolved{Point: Point{Latitude: 1, Longitude: 1, City: "IP City"}},
	})
	request := validRequest()
	request.Header.Set(HeaderCity, "Header City")
	point, resolved, err := source.EffectivePoint(request)
	if err != nil || point.City != "Header City" || point.Source != "device" {
		t.Fatalf("unexpected explicit result %#v %#v %v", point, resolved, err)
	}
}

func TestSourceReturnsPartialErrorForIncompleteHeaders(t *testing.T) {
	source := NewSource(NewIPExtractor("", nil), &fakeResolver{})
	request := validRequest()
	request.Header.Del(HeaderProvider)
	if _, _, err := source.EffectivePoint(request); !errors.Is(err, ErrPartial) {
		t.Fatalf("expected ErrPartial, got %v", err)
	}
}

func TestSourceFallsBackToIPInference(t *testing.T) {
	resolved := Resolved{Point: Point{Latitude: 22.5431, Longitude: 114.0579, City: "IP City",
		Source: "ip", Provider: "maxmind", Precision: "coarse"}}
	accuracy := 50
	resolved.AccuracyKm = &accuracy
	source := NewSource(NewIPExtractor("", nil), &fakeResolver{resolved: resolved})

	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "203.0.113.9:1234"
	point, gotResolved, err := source.EffectivePoint(request)
	if err != nil || point.Source != "ip" || point.City != "IP City" ||
		point.Latitude != 22.5 || point.Longitude != 114.1 {
		t.Fatalf("unexpected fallback result %#v %v", point, err)
	}
	if gotResolved.AccuracyKm == nil || *gotResolved.AccuracyKm != 50 {
		t.Fatalf("unexpected resolved accuracy %#v", gotResolved.AccuracyKm)
	}
}

func TestSourceRequiresHeadersWithoutResolver(t *testing.T) {
	source := NewSource(nil, nil)
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "203.0.113.9:1234"
	if _, _, err := source.EffectivePoint(request); !errors.Is(err, ErrRequired) {
		t.Fatalf("expected ErrRequired, got %v", err)
	}
	if source.Enabled() {
		t.Fatal("source without resolver must report disabled")
	}
}

func TestSourceMapsInferenceFailuresToUnavailable(t *testing.T) {
	for _, resolverErr := range []error{ErrNoClientIP, ErrLocationUnavailable, errors.New("db down")} {
		source := NewSource(NewIPExtractor("", nil), &fakeResolver{err: resolverErr})
		request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
		request.RemoteAddr = "203.0.113.9:1234"
		if _, _, err := source.EffectivePoint(request); !errors.Is(err, ErrLocationUnavailable) {
			t.Errorf("error %v: expected ErrLocationUnavailable, got %v", resolverErr, err)
		}
	}
}

func TestSourcePrefersExplicitHeadersOverCloudflare(t *testing.T) {
	source := NewSourceWithCloudflare(nil, nil, NewCloudflare(trustedNets))
	request := trustedCloudflareRequest()
	request.Header.Set(HeaderLatitude, "30.1")
	request.Header.Set(HeaderLongitude, "120.1")
	request.Header.Set(HeaderProvider, "ipinfo")
	point, _, err := source.EffectivePoint(request)
	if err != nil || point.Source != "device" || point.Provider != "ipinfo" {
		t.Fatalf("unexpected explicit result %#v %v", point, err)
	}
}

func TestSourceUsesCloudflareBeforeResolver(t *testing.T) {
	resolver := &fakeResolver{resolved: Resolved{Point: Point{Latitude: 1, Longitude: 1,
		Source: "ip", Provider: "maxmind", Precision: "coarse"}}}
	source := NewSourceWithCloudflare(nil, resolver, NewCloudflare(trustedNets))
	point, _, err := source.EffectivePoint(trustedCloudflareRequest())
	if err != nil || point.Provider != "cloudflare" {
		t.Fatalf("expected cloudflare source, got %#v %v", point, err)
	}
}

func TestSourceFailsClosedOnMalformedCloudflare(t *testing.T) {
	resolver := &fakeResolver{resolved: Resolved{Point: Point{Latitude: 1, Longitude: 1,
		Source: "ip", Provider: "maxmind", Precision: "coarse"}}}
	source := NewSourceWithCloudflare(nil, resolver, NewCloudflare(trustedNets))
	request := trustedCloudflareRequest()
	request.Header.Del(CloudflareHeaderLongitude)
	if _, _, err := source.EffectivePoint(request); !errors.Is(err, ErrLocationUnavailable) {
		t.Fatalf("malformed cloudflare bundle must fail closed, got %v", err)
	}
}

func TestSourceFallsBackToResolverWithoutCloudflareHeaders(t *testing.T) {
	resolver := &fakeResolver{resolved: Resolved{Point: Point{Latitude: 22.5431,
		Longitude: 114.0579, City: "IP City", Source: "ip", Provider: "maxmind",
		Precision: "coarse"}}}
	source := NewSourceWithCloudflare(NewIPExtractor("", nil), resolver, NewCloudflare(trustedNets))
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	point, _, err := source.EffectivePoint(request)
	if err != nil || point.Provider != "maxmind" || point.Key == "" {
		t.Fatalf("expected resolver fallback, got %#v %v", point, err)
	}
}

func TestSourceCloudflareOnlyRequiresHeaders(t *testing.T) {
	source := NewSourceWithCloudflare(nil, nil, NewCloudflare(trustedNets))
	if !source.Enabled() {
		t.Fatal("cloudflare-only source must report enabled")
	}
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	if _, _, err := source.EffectivePoint(request); !errors.Is(err, ErrLocationUnavailable) {
		t.Fatalf("missing cloudflare headers must be unavailable, got %v", err)
	}
}

func TestSourceUntrustedCloudflareHeadersAreIgnored(t *testing.T) {
	resolver := &fakeResolver{resolved: Resolved{Point: Point{Latitude: 22.5431,
		Longitude: 114.0579, City: "IP City", Source: "ip", Provider: "maxmind",
		Precision: "coarse"}}}
	source := NewSourceWithCloudflare(NewIPExtractor("", nil), resolver, NewCloudflare(trustedNets))
	request := trustedCloudflareRequest()
	request.RemoteAddr = "192.0.2.44:1234"
	point, _, err := source.EffectivePoint(request)
	if err != nil || point.Provider != "maxmind" {
		t.Fatalf("untrusted headers must be ignored, got %#v %v", point, err)
	}
}

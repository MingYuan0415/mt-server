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

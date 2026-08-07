package location

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFromRequestParsesAndNormalizesDeviceLocation(t *testing.T) {
	request := httptest.NewRequest("GET", "https://api.example.com/api/v1/weather/current", nil)
	request.Header.Set(HeaderLatitude, "22.5431")
	request.Header.Set(HeaderLongitude, "114.0579")
	request.Header.Set(HeaderProvider, "ipinfo")
	request.Header.Set(HeaderCity, " Shenzhen ")
	request.Header.Set(HeaderRegion, "Guangdong")
	request.Header.Set(HeaderCountry, "CN")
	request.Header.Set(HeaderTimezone, "Asia/Shanghai")
	request.Header.Set("CF-IPLatitude", "31.2304")
	request.Header.Set("X-Forwarded-For", "192.0.2.1")

	point, _, err := FromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if point.Latitude != 22.5 || point.Longitude != 114.1 || point.City != "Shenzhen" ||
		point.Provider != "ipinfo" || point.Source != "device" || point.Precision != "city" {
		t.Fatalf("unexpected point %#v", point)
	}
	if point.Key != "cc70e9b302c4b12b" {
		t.Fatalf("unexpected location key %q", point.Key)
	}
}

func TestNormalizeDerivesStableLocationKey(t *testing.T) {
	first, err := Normalize(Point{Latitude: 22.5431, Longitude: 114.0579})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Normalize(Point{Latitude: 22.5499, Longitude: 114.1499})
	if err != nil {
		t.Fatal(err)
	}
	if first.Key != second.Key || first.Key == "" {
		t.Fatalf("same grid must derive the same key: %q vs %q", first.Key, second.Key)
	}
	third, err := Normalize(Point{Latitude: 22.6, Longitude: 114.1})
	if err != nil {
		t.Fatal(err)
	}
	if third.Key == first.Key {
		t.Fatalf("different grid must derive a different key: %q", third.Key)
	}
	if len(first.Key) != 16 || first.Key != strings.ToLower(first.Key) {
		t.Fatalf("location key must be 16 lowercase hex digits: %q", first.Key)
	}
	if first.Key != locationKey(first.Latitude, first.Longitude) {
		t.Fatalf("location key must be derived from the canonical grid string: %q",
			first.Key)
	}
}

func TestFromRequestRequiresCoordinatesAndProvider(t *testing.T) {
	for _, missing := range []string{HeaderLatitude, HeaderLongitude, HeaderProvider} {
		t.Run(missing, func(t *testing.T) {
			request := validRequest()
			request.Header.Del(missing)
			if _, _, err := FromRequest(request); !errors.Is(err, ErrPartial) {
				t.Fatalf("expected partial error, got %v", err)
			}
		})
	}
}

func TestFromRequestAllowsAllHeadersAbsent(t *testing.T) {
	request := httptest.NewRequest("GET", "https://api.example.com/", nil)
	if _, explicit, err := FromRequest(request); err != nil || explicit {
		t.Fatalf("expected non-explicit absence, got %v %v", explicit, err)
	}
	request.Header.Set(HeaderCity, "City without coordinates")
	if _, explicit, err := FromRequest(request); err != nil || explicit {
		t.Fatalf("optional metadata alone must not trigger location parsing: %v %v", explicit, err)
	}
}

func TestFromRequestRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
		value  string
	}{
		{name: "latitude text", header: HeaderLatitude, value: "north"},
		{name: "latitude NaN", header: HeaderLatitude, value: "NaN"},
		{name: "longitude infinity", header: HeaderLongitude, value: "+Inf"},
		{name: "latitude range", header: HeaderLatitude, value: "91"},
		{name: "longitude range", header: HeaderLongitude, value: "-181"},
		{name: "coordinate too long", header: HeaderLatitude, value: strings.Repeat("1", 33)},
		{name: "provider uppercase", header: HeaderProvider, value: "IPInfo"},
		{name: "provider too long", header: HeaderProvider, value: "a" + strings.Repeat("b", 32)},
		{name: "metadata control", header: HeaderCity, value: "bad\tcity"},
		{name: "metadata too long", header: HeaderRegion, value: strings.Repeat("x", 129)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest()
			request.Header.Set(test.header, test.value)
			if _, _, err := FromRequest(request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid error, got %v", err)
			}
		})
	}

	request := validRequest()
	request.Header[http.CanonicalHeaderKey(HeaderCity)] = []string{string([]byte{0xff})}
	if _, _, err := FromRequest(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid UTF-8 error, got %v", err)
	}
}

func TestNormalizeAcceptsCoordinateBoundariesAndCanonicalizesZero(t *testing.T) {
	point, err := Normalize(Point{Latitude: math.Copysign(0, -1), Longitude: 180})
	if err != nil {
		t.Fatal(err)
	}
	if math.Signbit(point.Latitude) || point.Longitude != 180 {
		t.Fatalf("unexpected normalized boundary %#v", point)
	}
	zero, err := Normalize(Point{Latitude: math.Copysign(0, -1), Longitude: -0.0})
	if err != nil {
		t.Fatal(err)
	}
	if zero.Key != "3e0f3d3bb294400b" {
		t.Fatalf("negative zero must canonicalize to the zero-grid key, got %q", zero.Key)
	}
}

func TestChangeLimiterCountsOnlyGridChangesPerDevice(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	limiter := NewChangeLimiter(2, 5*time.Minute)
	limiter.now = func() time.Time { return now }
	if allowed, _ := limiter.Allow("one", "22.5,114.1"); !allowed {
		t.Fatal("initial grid was rejected")
	}
	if allowed, _ := limiter.Allow("one", "22.5,114.1"); !allowed {
		t.Fatal("same grid was rejected")
	}
	if allowed, _ := limiter.Allow("one", "22.6,114.1"); !allowed {
		t.Fatal("burst grid change was rejected")
	}
	if allowed, _ := limiter.Allow("one", "22.7,114.1"); !allowed {
		t.Fatal("second burst grid change was rejected")
	}
	if allowed, retry := limiter.Allow("one", "22.8,114.1"); allowed || retry != 5*time.Minute {
		t.Fatalf("unexpected exhausted result %v %v", allowed, retry)
	}
	if allowed, _ := limiter.Allow("two", "22.7,114.1"); !allowed {
		t.Fatal("second device did not receive an independent bucket")
	}
	now = now.Add(5 * time.Minute)
	if allowed, _ := limiter.Allow("one", "22.8,114.1"); !allowed {
		t.Fatal("refilled grid change was rejected")
	}
}

func TestChangeLimiterRetainRemovesRevokedDevices(t *testing.T) {
	limiter := NewChangeLimiter(1, time.Hour)
	if allowed, _ := limiter.Allow("revoked", "one"); !allowed {
		t.Fatal("initial grid was rejected")
	}
	limiter.Retain([]string{"active"})
	if allowed, _ := limiter.Allow("revoked", "two"); !allowed {
		t.Fatal("revoked device state was retained")
	}
}

func validRequest() *http.Request {
	request := httptest.NewRequest("GET", "https://api.example.com/", nil)
	request.Header.Set(HeaderLatitude, "22.5")
	request.Header.Set(HeaderLongitude, "114.1")
	request.Header.Set(HeaderProvider, "ipinfo")
	return request
}

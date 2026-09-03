package location

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

var trustedNets = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}

func newTestCloudflare() *Cloudflare {
	return NewCloudflare(trustedNets)
}

func trustedCloudflareRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	request.Header.Set(CloudflareHeaderLatitude, "22.5431")
	request.Header.Set(CloudflareHeaderLongitude, "114.0579")
	request.Header.Set(CloudflareHeaderCity, "Shenzhen")
	request.Header.Set(CloudflareHeaderRegion, "Guangdong")
	request.Header.Set(CloudflareHeaderCountry, "CN")
	request.Header.Set(CloudflareHeaderTimezone, "Asia/Shanghai")
	return request
}

func TestCloudflareParsesTrustedHeaders(t *testing.T) {
	point, present, err := newTestCloudflare().FromRequest(trustedCloudflareRequest())
	if err != nil || !present {
		t.Fatalf("unexpected result %v %v", present, err)
	}
	if point.Latitude != 22.54 || point.Longitude != 114.06 ||
		point.City != "Shenzhen" || point.Region != "Guangdong" ||
		point.Country != "CN" || point.Timezone != "Asia/Shanghai" {
		t.Fatalf("unexpected point %#v", point)
	}
	if point.Source != "ip" || point.Provider != "cloudflare" || point.Precision != "coarse" {
		t.Fatalf("unexpected source attribution %#v", point)
	}
	if point.Key == "" || len(point.Key) != 16 {
		t.Fatalf("unexpected location key %q", point.Key)
	}
	if point.Key != "6d3cb7ee7a8f3f1e" {
		t.Fatalf("unexpected location key %q", point.Key)
	}
}

func TestCloudflareIgnoresUntrustedPeers(t *testing.T) {
	request := trustedCloudflareRequest()
	request.RemoteAddr = "192.0.2.44:1234"
	if _, present, err := newTestCloudflare().FromRequest(request); err != nil || present {
		t.Fatalf("untrusted peer headers must be ignored, got %v %v", present, err)
	}
}

func TestCloudflareIgnoresMissingCoordinatePair(t *testing.T) {
	request := trustedCloudflareRequest()
	request.Header.Del(CloudflareHeaderLatitude)
	request.Header.Del(CloudflareHeaderLongitude)
	if _, present, err := newTestCloudflare().FromRequest(request); err != nil || present {
		t.Fatalf("missing pair must not activate, got %v %v", present, err)
	}
}

func TestCloudflareIgnoresCountryOnlyHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	request.Header.Set(CloudflareHeaderCountry, "CN")
	if _, present, err := newTestCloudflare().FromRequest(request); err != nil || present {
		t.Fatalf("country-only headers must not activate, got %v %v", present, err)
	}
}

func TestCloudflareRejectsPartialCoordinatePair(t *testing.T) {
	request := trustedCloudflareRequest()
	request.Header.Del(CloudflareHeaderLongitude)
	if _, _, err := newTestCloudflare().FromRequest(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial pair must fail closed, got %v", err)
	}
}

func TestCloudflareRejectsDuplicateHeaders(t *testing.T) {
	request := trustedCloudflareRequest()
	request.Header.Add(CloudflareHeaderLatitude, "31.2304")
	if _, _, err := newTestCloudflare().FromRequest(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate headers must fail closed, got %v", err)
	}
}

func TestCloudflareRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
		value  string
	}{
		{name: "latitude text", header: CloudflareHeaderLatitude, value: "north"},
		{name: "latitude NaN", header: CloudflareHeaderLatitude, value: "NaN"},
		{name: "longitude infinity", header: CloudflareHeaderLongitude, value: "+Inf"},
		{name: "latitude range", header: CloudflareHeaderLatitude, value: "91"},
		{name: "longitude range", header: CloudflareHeaderLongitude, value: "-181"},
		{name: "blank latitude", header: CloudflareHeaderLatitude, value: " "},
		{name: "metadata control", header: CloudflareHeaderCity, value: "bad\tcity"},
		{name: "metadata too long", header: CloudflareHeaderRegion, value: strings.Repeat("x", 129)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := trustedCloudflareRequest()
			request.Header.Set(test.header, test.value)
			if _, _, err := newTestCloudflare().FromRequest(request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid error, got %v", err)
			}
		})
	}

	request := trustedCloudflareRequest()
	request.Header[http.CanonicalHeaderKey(CloudflareHeaderCity)] = []string{string([]byte{0xff})}
	if _, _, err := newTestCloudflare().FromRequest(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid UTF-8 error, got %v", err)
	}
}

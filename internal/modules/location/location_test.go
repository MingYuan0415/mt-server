package locationmod

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

const testDeviceToken = "high-entropy-device-token"

type fakeResolver struct {
	resolved location.Resolved
	err      error
}

func (f *fakeResolver) Resolve(netip.Addr) (location.Resolved, error) {
	return f.resolved, f.err
}

func TestLocationModuleRequiresAuthentication(t *testing.T) {
	handler := testLocationHandler(&fakeResolver{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "https://api.example.com/api/v1/location", nil))
	if recorder.Code != http.StatusUnauthorized ||
		!strings.Contains(recorder.Body.String(), "unauthorized") {
		t.Fatalf("unexpected response %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLocationModuleReturnsDisplaySafeMetadata(t *testing.T) {
	accuracy := 50
	resolved := location.Resolved{
		Point: location.Point{
			Latitude: 22.5431, Longitude: 114.0579, City: "Shenzhen",
			Region: "Guangdong", Country: "CN", Timezone: "Asia/Shanghai",
			Source: "ip", Provider: "maxmind", Precision: "coarse",
		},
		AccuracyKm: &accuracy,
	}
	handler := testLocationHandler(&fakeResolver{resolved: resolved})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/location", nil)
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["schema_version"] != float64(1) {
		t.Fatalf("unexpected schema version %#v", response["schema_version"])
	}
	publicLocation := response["location"].(map[string]any)
	if publicLocation["city"] != "Shenzhen" || publicLocation["region"] != "Guangdong" ||
		publicLocation["country"] != "CN" || publicLocation["timezone"] != "Asia/Shanghai" ||
		publicLocation["source"] != "ip" || publicLocation["provider"] != "maxmind" ||
		publicLocation["precision"] != "coarse" {
		t.Fatalf("unexpected location %#v", publicLocation)
	}
	key, _ := publicLocation["location_key"].(string)
	if len(key) != 16 {
		t.Fatalf("unexpected location key %q", key)
	}
	if response["accuracy_radius_km"] != float64(50) {
		t.Fatalf("unexpected accuracy %#v", response["accuracy_radius_km"])
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"latitude", "longitude", "22.5", "114.1", "192.0.2.1"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestLocationModuleReportsUnavailableWithoutInference(t *testing.T) {
	handler := testLocationHandler(nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/location", nil)
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable ||
		!strings.Contains(recorder.Body.String(), "location_unavailable") {
		t.Fatalf("unexpected response %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLocationModuleRejectsInferenceFailures(t *testing.T) {
	handler := testLocationHandler(&fakeResolver{err: location.ErrLocationUnavailable})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/location", nil)
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected response %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLocationModuleFailsClosedOnMalformedTrustedCloudflareHeaders(t *testing.T) {
	source := location.NewSourceWithCloudflare(nil, nil, location.NewCloudflare(
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}))
	handler := testLocationHandlerWithSource(source)
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/location", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	request.Header.Set("CF-IPLatitude", "22.5431")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable ||
		!strings.Contains(recorder.Body.String(), "location_unavailable") {
		t.Fatalf("malformed trusted headers must fail closed, got %d %s",
			recorder.Code, recorder.Body.String())
	}
}

func TestLocationModuleIgnoresUntrustedCloudflareHeaders(t *testing.T) {
	source := location.NewSourceWithCloudflare(
		location.NewIPExtractor("", nil), &fakeResolver{
			resolved: location.Resolved{Point: location.Point{
				Latitude: 22.5431, Longitude: 114.0579, City: "Shenzhen",
				Source: "ip", Provider: "maxmind", Precision: "coarse",
			}},
		}, location.NewCloudflare([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}))
	handler := testLocationHandlerWithSource(source)
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/location", nil)
	request.RemoteAddr = "192.0.2.44:1234"
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	request.Header.Set("CF-IPLatitude", "garbage")
	request.Header.Set("CF-IPLongitude", "also-bad")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK ||
		!strings.Contains(recorder.Body.String(), `"provider":"maxmind"`) {
		t.Fatalf("untrusted headers must be ignored, got %d %s",
			recorder.Code, recorder.Body.String())
	}
}

func TestLocationModuleRejectsUnauthenticatedCloudflareHeaders(t *testing.T) {
	source := location.NewSourceWithCloudflare(nil, nil, location.NewCloudflare(
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}))
	handler := testLocationHandlerWithSource(source)
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/location", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	request.Header.Set("CF-IPLatitude", "22.5431")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("authentication must precede location parsing, got %d %s",
			recorder.Code, recorder.Body.String())
	}
}

func testLocationHandler(resolver location.Resolver) http.Handler {
	var source *location.Source
	if resolver != nil {
		source = location.NewSource(location.NewIPExtractor("", nil), resolver)
	} else {
		source = location.NewSource(nil, nil)
	}
	return testLocationHandlerWithSource(source)
}

func testLocationHandlerWithSource(source *location.Source) http.Handler {
	module := NewModule(source)
	mux := http.NewServeMux()
	module.RegisterRoutes(mux)
	return auth.New(testDeviceToken).Wrap(mux)
}

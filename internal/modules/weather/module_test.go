package weather

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

const testDeviceToken = "high-entropy-device-token"

func TestModuleReturnsStablePrivacyReducedEnvelope(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{fetch: successfulFetch(now)}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	defer closeService(t, service)
	handler := testDeviceHandler(service, 4)

	request := deviceRequest("/api/v1/weather/current?latitude=31&longitude=121")
	request.Header.Set(location.HeaderCity, "Shenzhen")
	request.Header.Set(location.HeaderRegion, "Guangdong")
	request.Header.Set(location.HeaderCountry, "CN")
	request.Header.Set("CF-IPLatitude", "31.2304")
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unexpected cache policy %q", recorder.Header().Get("Cache-Control"))
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	source := response["source"].(map[string]any)
	if response["schema_version"] != float64(1) || source["name"] != "QWeather" {
		t.Fatalf("unexpected weather envelope %#v", response)
	}
	publicLocation := response["location"].(map[string]any)
	if publicLocation["source"] != "device" || publicLocation["provider"] != "ipinfo" ||
		publicLocation["precision"] != "city" || publicLocation["city"] != "Shenzhen" {
		t.Fatalf("unexpected location %#v", publicLocation)
	}
	key, _ := publicLocation["location_key"].(string)
	if len(key) != 16 {
		t.Fatalf("unexpected location key %q", key)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"latitude", "longitude", "CF-IP", "X-Forwarded", "192.0.2.1"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestModuleAuthenticatesBeforeLocationValidation(t *testing.T) {
	service := newTestService(&fakeProvider{fetch: successfulFetch(time.Now())}, 64)
	defer closeService(t, service)
	handler := testDeviceHandler(service, 4)
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "unauthorized") {
		t.Fatalf("unexpected response %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestModuleRejectsMissingAndInvalidLocation(t *testing.T) {
	var fetches atomic.Int32
	provider := &fakeProvider{fetch: func(context.Context, Kind, location.Point) (ProviderResult, error) {
		fetches.Add(1)
		return successfulFetch(time.Now())(context.Background(), KindCurrent, location.Point{})
	}}
	service := newTestService(provider, 64)
	defer closeService(t, service)
	handler := testDeviceHandler(service, 4)

	missing := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
	missing.Header.Set("Authorization", "Bearer "+testDeviceToken)
	missingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(missingRecorder, missing)
	if missingRecorder.Code != http.StatusBadRequest ||
		!strings.Contains(missingRecorder.Body.String(), "location_required") {
		t.Fatalf("unexpected missing-location response %d %s",
			missingRecorder.Code, missingRecorder.Body.String())
	}

	partial := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
	partial.Header.Set("Authorization", "Bearer "+testDeviceToken)
	partial.Header.Set(location.HeaderLatitude, "22.5")
	partialRecorder := httptest.NewRecorder()
	handler.ServeHTTP(partialRecorder, partial)
	if partialRecorder.Code != http.StatusBadRequest ||
		!strings.Contains(partialRecorder.Body.String(), "invalid_location") {
		t.Fatalf("unexpected partial-location response %d %s",
			partialRecorder.Code, partialRecorder.Body.String())
	}

	invalid := deviceRequest("/api/v1/weather/current")
	invalid.Header.Set(location.HeaderLatitude, "NaN")
	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, invalid)
	if invalidRecorder.Code != http.StatusBadRequest ||
		!strings.Contains(invalidRecorder.Body.String(), "invalid_location") {
		t.Fatalf("unexpected invalid-location response %d %s",
			invalidRecorder.Code, invalidRecorder.Body.String())
	}
	if fetches.Load() != 0 {
		t.Fatalf("invalid locations reached the provider %d times", fetches.Load())
	}
}

func TestModuleFallsBackToIPInferenceWithoutHeaders(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{fetch: successfulFetch(now)}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	defer closeService(t, service)
	source := location.NewSource(location.NewIPExtractor("", nil), &fakeResolver{
		resolved: location.Resolved{Point: location.Point{
			Latitude: 22.5431, Longitude: 114.0579, City: "IP City",
			Source: "ip", Provider: "maxmind", Precision: "coarse",
		}},
	})
	handler := testDeviceHandlerWithSource(service, 4, source)

	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	request.Header.Set("CF-IPLatitude", "31.2304")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	publicLocation := response["location"].(map[string]any)
	if publicLocation["source"] != "ip" || publicLocation["provider"] != "maxmind" ||
		publicLocation["precision"] != "coarse" || publicLocation["city"] != "IP City" {
		t.Fatalf("unexpected IP location %#v", publicLocation)
	}
	key, _ := publicLocation["location_key"].(string)
	if len(key) != 16 {
		t.Fatalf("unexpected location key %q", key)
	}
	for _, forbidden := range []string{"latitude", "longitude", "22.5", "114.1"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestModuleFailsClosedOnMalformedTrustedCloudflareHeaders(t *testing.T) {
	var fetches atomic.Int32
	provider := &fakeProvider{fetch: func(context.Context, Kind, location.Point) (ProviderResult, error) {
		fetches.Add(1)
		return successfulFetch(time.Now())(context.Background(), KindCurrent, location.Point{})
	}}
	service := newTestService(provider, 64)
	defer closeService(t, service)
	source := location.NewSourceWithCloudflare(nil, nil, location.NewCloudflare(
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}))
	handler := testDeviceHandlerWithSource(service, 4, source)

	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
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
	if fetches.Load() != 0 {
		t.Fatalf("malformed cloudflare location reached the provider %d times", fetches.Load())
	}
}

func TestModuleIgnoresMalformedUntrustedCloudflareHeaders(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{fetch: successfulFetch(now)}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	defer closeService(t, service)
	source := location.NewSourceWithCloudflare(
		location.NewIPExtractor("", nil), &fakeResolver{
			resolved: location.Resolved{Point: location.Point{
				Latitude: 22.5431, Longitude: 114.0579, City: "IP City",
				Source: "ip", Provider: "maxmind", Precision: "coarse",
			}},
		}, location.NewCloudflare([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}))
	handler := testDeviceHandlerWithSource(service, 4, source)

	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
	request.RemoteAddr = "192.0.2.44:1234"
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	request.Header.Set("CF-IPLatitude", "garbage")
	request.Header.Set("CF-IPLongitude", "also-bad")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("untrusted headers must be ignored, got %d %s",
			recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"provider":"maxmind"`) {
		t.Fatalf("expected resolver fallback: %s", recorder.Body.String())
	}
}

func TestModuleRejectsUnauthenticatedCloudflareHeaders(t *testing.T) {
	service := newTestService(&fakeProvider{fetch: successfulFetch(time.Now())}, 64)
	defer closeService(t, service)
	source := location.NewSourceWithCloudflare(nil, nil, location.NewCloudflare(
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}))
	handler := testDeviceHandlerWithSource(service, 4, source)

	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
	request.RemoteAddr = "10.1.2.3:54321"
	request.Header.Set("CF-IPLatitude", "22.5431")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("authentication must precede location parsing, got %d %s",
			recorder.Code, recorder.Body.String())
	}
}

func TestModuleMapsUnavailableInferenceToServiceUnavailable(t *testing.T) {
	provider := &fakeProvider{fetch: successfulFetch(time.Now())}
	service := newTestService(provider, 64)
	defer closeService(t, service)
	source := location.NewSource(location.NewIPExtractor("", nil), &fakeResolver{
		err: location.ErrLocationUnavailable,
	})
	handler := testDeviceHandlerWithSource(service, 4, source)
	request := httptest.NewRequest(http.MethodGet,
		"https://api.example.com/api/v1/weather/current", nil)
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable ||
		!strings.Contains(recorder.Body.String(), "location_unavailable") {
		t.Fatalf("unexpected unavailable response %d %s", recorder.Code, recorder.Body.String())
	}
}

type fakeResolver struct {
	resolved location.Resolved
	err      error
}

func (f *fakeResolver) Resolve(netip.Addr) (location.Resolved, error) {
	return f.resolved, f.err
}

func testDeviceHandlerWithSource(service *Service, capacity int, source *location.Source) http.Handler {
	module := NewModule(service, location.NewChangeLimiter(capacity, 5*time.Minute),
		source, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	module.RegisterRoutes(mux)
	return auth.New(testDeviceToken).Wrap(mux)
}

func TestModuleExposesAllWeatherDatasets(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{fetch: func(_ context.Context, kind Kind,
		_ location.Point) (ProviderResult, error) {
		switch kind {
		case KindCurrent:
			return ProviderResult{UpdatedAt: now, Data: Current{TemperatureC: 20}}, nil
		case KindHourly:
			return ProviderResult{UpdatedAt: now, Data: Hourly{Hours: []Hour{{ForecastAt: now}}}}, nil
		case KindDaily:
			return ProviderResult{UpdatedAt: now, Data: Daily{Days: []Day{{Date: "2026-08-02"}}}}, nil
		case KindAlerts:
			return ProviderResult{UpdatedAt: now, Data: Alerts{Items: []Alert{}}}, nil
		default:
			return ProviderResult{}, errors.New("unexpected weather kind")
		}
	}}
	service := newTestService(provider, 64)
	service.now = func() time.Time { return now }
	defer closeService(t, service)
	handler := testDeviceHandler(service, 4)

	for _, test := range []struct {
		path      string
		dataField string
	}{
		{path: "/api/v1/weather/current", dataField: "temperature_c"},
		{path: "/api/v1/weather/hourly", dataField: "hours"},
		{path: "/api/v1/weather/daily", dataField: "days"},
		{path: "/api/v1/weather/alerts", dataField: "items"},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, deviceRequest(test.path))
			if recorder.Code != http.StatusOK {
				t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if _, ok := response.Data[test.dataField]; !ok {
				t.Fatalf("response data does not contain %q: %s", test.dataField, recorder.Body.String())
			}
		})
	}
}

func TestModuleDoesNotCacheRequestMetadata(t *testing.T) {
	var fetches atomic.Int32
	provider := &fakeProvider{fetch: func(ctx context.Context, kind Kind,
		point location.Point) (ProviderResult, error) {
		fetches.Add(1)
		return successfulFetch(time.Now())(ctx, kind, point)
	}}
	service := newTestService(provider, 64)
	defer closeService(t, service)
	handler := testDeviceHandler(service, 4)
	for _, city := range []string{"First", "Second"} {
		request := deviceRequest("/api/v1/weather/current")
		request.Header.Set(location.HeaderCity, city)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"city":"`+city+`"`) {
			t.Fatalf("unexpected metadata response %d %s", recorder.Code, recorder.Body.String())
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("same grid fetched provider %d times", fetches.Load())
	}
}

func TestModuleRateLimitsLocationChanges(t *testing.T) {
	service := newTestService(&fakeProvider{fetch: successfulFetch(time.Now())}, 64)
	defer closeService(t, service)
	handler := testDeviceHandler(service, 1)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, deviceRequest("/api/v1/weather/current"))
	secondRequest := deviceRequest("/api/v1/weather/current")
	secondRequest.Header.Set(location.HeaderLatitude, "23.5")
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, secondRequest)
	thirdRequest := deviceRequest("/api/v1/weather/current")
	thirdRequest.Header.Set(location.HeaderLatitude, "24.5")
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, thirdRequest)
	if first.Code != http.StatusOK || second.Code != http.StatusOK ||
		third.Code != http.StatusTooManyRequests || third.Header().Get("Retry-After") == "" ||
		!strings.Contains(third.Body.String(), "location_rate_limited") {
		t.Fatalf("unexpected rate-limit responses %d %d %d %s",
			first.Code, second.Code, third.Code, third.Body.String())
	}
}

func TestModuleMapsProviderTimeoutToGatewayTimeout(t *testing.T) {
	provider := &fakeProvider{fetch: func(context.Context, Kind,
		location.Point) (ProviderResult, error) {
		return ProviderResult{}, context.DeadlineExceeded
	}}
	service := newTestService(provider, 64)
	defer closeService(t, service)
	recorder := httptest.NewRecorder()
	testDeviceHandler(service, 4).ServeHTTP(recorder, deviceRequest("/api/v1/weather/current"))
	if recorder.Code != http.StatusGatewayTimeout ||
		!strings.Contains(recorder.Body.String(), "upstream_timeout") {
		t.Fatalf("unexpected timeout response %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestModuleLifecycle(t *testing.T) {
	provider := &fakeProvider{fetch: successfulFetch(time.Now())}
	service := newTestService(provider, 64)
	module := NewModule(service, location.NewChangeLimiter(4, time.Minute),
		location.NewSource(nil, nil), slog.Default())
	if module.Name() != "weather" || module.Start(context.Background()) != nil || module.Ready() != nil {
		t.Fatal("unexpected module lifecycle result")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := module.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func testDeviceHandler(service *Service, capacity int) http.Handler {
	module := NewModule(service, location.NewChangeLimiter(capacity, 5*time.Minute),
		location.NewSource(nil, nil),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	module.RegisterRoutes(mux)
	return auth.New(testDeviceToken).Wrap(mux)
}

func deviceRequest(path string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com"+path, nil)
	request.Header.Set("Authorization", "Bearer "+testDeviceToken)
	request.Header.Set(location.HeaderLatitude, "22.5431")
	request.Header.Set(location.HeaderLongitude, "114.0579")
	request.Header.Set(location.HeaderProvider, "ipinfo")
	return request
}

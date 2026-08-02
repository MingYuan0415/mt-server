package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
	providerqweather "github.com/MingYuan0415/mt-server/internal/providers/qweather"
)

type fakeRuntime struct {
	testError error
	testCalls int
	applied   state.State
	tokens    []state.DeviceToken
	testPoint *location.Point
}

func (f *fakeRuntime) Test(_ context.Context, _ state.State,
	point *location.Point) (weather.Verification, string, error) {
	f.testCalls++
	f.testPoint = point
	return testVerification(), "test-fingerprint", f.testError
}
func (f *fakeRuntime) Apply(value state.State) error { f.applied = value; return nil }
func (f *fakeRuntime) ReplaceTokens(tokens []state.DeviceToken) error {
	f.tokens = append([]state.DeviceToken(nil), tokens...)
	return nil
}
func (f *fakeRuntime) Ready() error { return nil }

func testVerification() weather.Verification {
	return weather.Verification{
		Source: weather.Source{ID: "qweather", Name: "QWeather", AttributionURL: "https://www.qweather.com/"},
		Location: weather.PublicLocation{
			City: "Example", Source: "browser", Provider: "browser", Precision: "coarse",
		},
		TestedAt:  time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 2, 0, 55, 0, 0, time.UTC),
		Data: weather.Current{TemperatureC: 28, FeelsLikeC: 31, ConditionCode: "101",
			ConditionText: "Cloudy", HumidityPercent: 72, WindSpeedKMH: 8},
	}
}

func TestSetupPersistsHashedSecretsAndCreatesSession(t *testing.T) {
	handler, store, runtime, publicCSRF := newTestHandler(t)
	recorder := performSetup(t, handler, publicCSRF)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("unexpected setup response %d %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		CSRFToken    string               `json:"csrf_token"`
		DeviceToken  string               `json:"device_token"`
		Verification weather.Verification `json:"verification"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.CSRFToken == "" || !strings.HasPrefix(response.DeviceToken, "mt_") ||
		len(recorder.Result().Cookies()) == 0 || response.Verification.Data.ConditionCode != "101" {
		t.Fatalf("incomplete setup response %#v", response)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := json.Marshal(loaded)
	if strings.Contains(string(contents), response.DeviceToken) ||
		loaded.DeviceTokens[0].Hash == response.DeviceToken {
		t.Fatal("persistent state contains a raw device token")
	}
	if strings.Contains(string(contents), `"location"`) {
		t.Fatalf("setup persisted temporary location data: %s", contents)
	}
	if runtime.testPoint == nil || runtime.testPoint.Latitude != 30.2 || runtime.testPoint.Longitude != 120.1 {
		t.Fatalf("temporary test location was not passed to runtime: %#v", runtime.testPoint)
	}
	if strings.Contains(recorder.Body.String(), `"latitude"`) ||
		strings.Contains(recorder.Body.String(), `"longitude"`) {
		t.Fatalf("setup response exposed test coordinates: %s", recorder.Body.String())
	}
	if runtime.applied.SchemaVersion != state.SchemaVersion {
		t.Fatal("runtime was not activated")
	}
	cookie := recorder.Result().Cookies()[0]
	qweather := authenticatedRequest(t, handler, http.MethodGet,
		"/admin/api/v1/settings/qweather", nil, cookie, response.CSRFToken)
	if strings.Contains(qweather.Body.String(), "test-private-key") ||
		strings.Contains(qweather.Body.String(), "private_key_pem") {
		t.Fatalf("QWeather response exposed private key material: %s", qweather.Body.String())
	}
	tokens := authenticatedRequest(t, handler, http.MethodGet,
		"/admin/api/v1/device-tokens", nil, cookie, response.CSRFToken)
	if strings.Contains(tokens.Body.String(), response.DeviceToken) ||
		strings.Contains(tokens.Body.String(), `"hash"`) {
		t.Fatalf("device-token list exposed a secret verifier: %s", tokens.Body.String())
	}
	second := performSetup(t, handler, publicCSRF)
	if second.Code != http.StatusConflict {
		t.Fatalf("repeated setup was accepted: %d", second.Code)
	}
	if runtime.testCalls != 1 {
		t.Fatalf("repeated setup called QWeather %d times", runtime.testCalls)
	}
}

func TestSetupMapsQWeatherCredentialFailure(t *testing.T) {
	handler, store, runtime, publicCSRF := newTestHandler(t)
	runtime.testError = &providerqweather.UpstreamError{HTTPStatus: http.StatusUnauthorized}
	recorder := performSetup(t, handler, publicCSRF)
	if recorder.Code != http.StatusBadGateway ||
		!strings.Contains(recorder.Body.String(), "qweather_credentials_rejected") {
		t.Fatalf("unexpected credential failure %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := store.Load(); !errors.Is(err, state.ErrNotInitialized) {
		t.Fatalf("failed setup persisted state: %v", err)
	}
}

func TestSetupReportsMissingTestLocation(t *testing.T) {
	handler, _, runtime, publicCSRF := newTestHandler(t)
	requestBody := validSetupRequest()
	requestBody.QWeather.TestLocation = &testLocationInput{}
	recorder := performSetupRequest(t, handler, publicCSRF, requestBody)
	if recorder.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(recorder.Body.String(), "test_location_unavailable") {
		t.Fatalf("unexpected location failure %d %s", recorder.Code, recorder.Body.String())
	}
	if runtime.testCalls != 0 {
		t.Fatalf("incomplete test location reached runtime %d times", runtime.testCalls)
	}
}

func TestQWeatherTestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "forbidden", err: &providerqweather.UpstreamError{HTTPStatus: http.StatusForbidden},
			status: http.StatusBadGateway, code: "qweather_credentials_rejected"},
		{name: "business unauthorized", err: &providerqweather.UpstreamError{Code: "401"},
			status: http.StatusBadGateway, code: "qweather_credentials_rejected"},
		{name: "rate limited", err: &providerqweather.UpstreamError{HTTPStatus: http.StatusTooManyRequests},
			status: http.StatusTooManyRequests, code: "qweather_rate_limited"},
		{name: "upstream failure", err: &providerqweather.UpstreamError{HTTPStatus: http.StatusBadGateway},
			status: http.StatusBadGateway, code: "qweather_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://api.example.com/admin/api/v1/setup", nil)
			recorder := httptest.NewRecorder()
			(&Handler{}).writeTestError(recorder, request, test.err, true)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.code) ||
				!strings.Contains(recorder.Body.String(), "retained") {
				t.Fatalf("unexpected mapped error %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestQWeatherFailureRetainsExistingState(t *testing.T) {
	handler, store, runtime, publicCSRF := newTestHandler(t)
	setup := performSetup(t, handler, publicCSRF)
	var setupBody map[string]any
	_ = json.Unmarshal(setup.Body.Bytes(), &setupBody)
	cookie := setup.Result().Cookies()[0]
	before, _ := store.Load()
	runtime.testError = errors.New("upstream failed")
	body := qweatherInput{
		APIHost: "new.re.qweatherapi.com", ProjectID: "new", CredentialID: "new",
		TestLocation: validTestLocation(),
	}
	recorder := authenticatedRequest(t, handler, http.MethodPut,
		"/admin/api/v1/settings/qweather", body, cookie, setupBody["csrf_token"].(string))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("unexpected update response %d %s", recorder.Code, recorder.Body.String())
	}
	after, _ := store.Load()
	if after.QWeather.APIHost != before.QWeather.APIHost {
		t.Fatal("failed QWeather test replaced persistent settings")
	}
}

func TestDeviceTokenOverlapAndRevocation(t *testing.T) {
	handler, store, _, publicCSRF := newTestHandler(t)
	setup := performSetup(t, handler, publicCSRF)
	var setupBody map[string]any
	_ = json.Unmarshal(setup.Body.Bytes(), &setupBody)
	cookie := setup.Result().Cookies()[0]
	csrf := setupBody["csrf_token"].(string)
	created := authenticatedRequest(t, handler, http.MethodPost,
		"/admin/api/v1/device-tokens", tokenRequest{Name: "Second device"}, cookie, csrf)
	if created.Code != http.StatusCreated {
		t.Fatalf("unexpected create response %d %s", created.Code, created.Body.String())
	}
	value, _ := store.Load()
	if len(value.DeviceTokens) != 2 {
		t.Fatalf("expected overlapping tokens, got %d", len(value.DeviceTokens))
	}
	revoked := authenticatedRequest(t, handler, http.MethodDelete,
		"/admin/api/v1/device-tokens/"+value.DeviceTokens[0].ID, nil, cookie, csrf)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("unexpected revoke response %d %s", revoked.Code, revoked.Body.String())
	}
	value, _ = store.Load()
	if len(value.DeviceTokens) != 1 {
		t.Fatalf("expected one token after revoke, got %d", len(value.DeviceTokens))
	}
}

func TestManagementWriteRequiresOriginAndCSRF(t *testing.T) {
	handler, _, _, publicCSRF := newTestHandler(t)
	setup := performSetup(t, handler, publicCSRF)
	cookie := setup.Result().Cookies()[0]
	request := httptest.NewRequest(http.MethodPost,
		"http://api.example.com/admin/api/v1/device-tokens", strings.NewReader(`{"name":"Device"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("write without origin/CSRF returned %d", recorder.Code)
	}
}

func TestPublicManagementWriteRequiresPreAuthenticationCSRF(t *testing.T) {
	handler, _, _, _ := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost,
		"http://api.example.com/admin/api/v1/setup", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://api.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "csrf_rejected") {
		t.Fatalf("setup without CSRF returned %d %s", recorder.Code, recorder.Body.String())
	}
}

func newTestHandler(t *testing.T) (http.Handler, *state.Store, *fakeRuntime, string) {
	t.Helper()
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	management, err := New(store, runtime, adminauth.NewSessions(),
		adminauth.NewTransportPolicy(true, false),
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	management.RegisterRoutes(mux)
	return mux, store, runtime, management.publicCSRF
}

func performSetup(t *testing.T, handler http.Handler, publicCSRF string) *httptest.ResponseRecorder {
	t.Helper()
	return performSetupRequest(t, handler, publicCSRF, validSetupRequest())
}

func validSetupRequest() setupRequest {
	return setupRequest{
		Password: "correct horse battery staple",
		QWeather: qweatherInput{
			APIHost: "account.re.qweatherapi.com", ProjectID: "project",
			CredentialID: "credential", PrivateKeyPEM: "test-private-key",
			TestLocation: validTestLocation(),
		},
		DeviceName: "MicroTech",
	}
}

func validTestLocation() *testLocationInput {
	latitude := 30.2
	longitude := 120.1
	return &testLocationInput{Latitude: &latitude, Longitude: &longitude, City: "Example"}
}

func performSetupRequest(t *testing.T, handler http.Handler, publicCSRF string,
	requestBody setupRequest) *httptest.ResponseRecorder {
	t.Helper()
	contents, _ := json.Marshal(requestBody)
	request := httptest.NewRequest(http.MethodPost,
		"http://api.example.com/admin/api/v1/setup", bytes.NewReader(contents))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://api.example.com")
	request.Header.Set("X-CSRF-Token", publicCSRF)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func authenticatedRequest(t *testing.T, handler http.Handler, method, path string,
	body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		contents, _ := json.Marshal(body)
		reader = bytes.NewReader(contents)
	}
	request := httptest.NewRequest(method, "http://api.example.com"+path, reader)
	request.Header.Set("Origin", "http://api.example.com")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

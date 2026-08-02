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
	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
	providerqweather "github.com/MingYuan0415/mt-server/internal/providers/qweather"
)

type fakeRuntime struct {
	testError   error
	testCalls   int
	applied     state.State
	tokens      []state.DeviceToken
	testPoint   *location.Point
	testStarted chan struct{}
	testRelease chan struct{}
}

type fakePreparedChange struct {
	activate func()
}

func (f *fakePreparedChange) Activate() {
	if f.activate != nil {
		f.activate()
		f.activate = nil
	}
}

func (f *fakePreparedChange) Discard() { f.activate = nil }

func (f *fakeRuntime) Test(_ context.Context, _ state.State,
	point *location.Point) (weather.Verification, string, error) {
	f.testCalls++
	f.testPoint = point
	if f.testStarted != nil {
		select {
		case f.testStarted <- struct{}{}:
		default:
		}
		<-f.testRelease
	}
	return testVerification(), "test-fingerprint", f.testError
}
func (f *fakeRuntime) Prepare(value state.State) (platform.PreparedChange, error) {
	return &fakePreparedChange{activate: func() { f.applied = value }}, nil
}
func (f *fakeRuntime) PrepareTokens(tokens []state.DeviceToken) (platform.PreparedChange, error) {
	prepared := append([]state.DeviceToken(nil), tokens...)
	return &fakePreparedChange{activate: func() { f.tokens = prepared }}, nil
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
	request := httptest.NewRequest(http.MethodPost, "https://api.example.com/admin/api/v1/setup", nil)
	recorder := httptest.NewRecorder()
	(&Handler{}).writeTestError(recorder, request, &providerqweather.UpstreamError{
		HTTPStatus: http.StatusTooManyRequests, Delay: 2 * time.Minute,
	}, false)
	if recorder.Header().Get("Retry-After") != "120" {
		t.Fatalf("QWeather Retry-After was not forwarded: %v", recorder.Header())
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

func TestQWeatherPersistenceFailureDoesNotActivatePreparedRuntime(t *testing.T) {
	handler, store, runtime, publicCSRF := newTestHandler(t)
	setup := performSetup(t, handler, publicCSRF)
	var setupBody map[string]any
	if err := json.Unmarshal(setup.Body.Bytes(), &setupBody); err != nil {
		t.Fatal(err)
	}
	cookie := setup.Result().Cookies()[0]
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	store.RenameForTest(func(string, string) error { return errors.New("injected rename failure") })
	input := qweatherInput{
		APIHost: "candidate.re.qweatherapi.com", ProjectID: "candidate",
		CredentialID: "candidate", TestLocation: validTestLocation(),
	}
	recorder := authenticatedRequest(t, handler, http.MethodPut,
		"/admin/api/v1/settings/qweather", input, cookie, setupBody["csrf_token"].(string))
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(recorder.Body.String(), "state_write_failed") {
		t.Fatalf("unexpected persistence failure: %d %s", recorder.Code, recorder.Body.String())
	}
	after, err := store.Load()
	if err != nil || after.QWeather.APIHost != before.QWeather.APIHost {
		t.Fatalf("failed persistence changed state: %#v %v", after, err)
	}
	if runtime.applied.QWeather.APIHost != before.QWeather.APIHost {
		t.Fatalf("prepared runtime was activated after failed persistence: %#v", runtime.applied.QWeather)
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
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "origin_rejected") {
		t.Fatalf("write without origin/CSRF returned %d %s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost,
		"http://api.example.com/admin/api/v1/device-tokens", strings.NewReader(`{"name":"Device"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://api.example.com")
	request.Header.Set("X-CSRF-Token", "wrong")
	request.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "csrf_rejected") {
		t.Fatalf("write with bad CSRF returned %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestManagementRejectionLogOmitsRequestMetadataAndSecrets(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	management, err := New(store, &fakeRuntime{}, adminauth.NewSessions(),
		adminauth.NewTransportPolicy(true, false), logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	management.RegisterRoutes(mux)
	request := httptest.NewRequest(http.MethodPost,
		"http://internal.example/admin/api/v1/setup", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://untrusted.example")
	request.Header.Set("X-CSRF-Token", management.publicCSRF)
	recorder := httptest.NewRecorder()
	httpapi.RequestContext(logger, mux).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unexpected rejection status %d", recorder.Code)
	}
	logValue := logs.String()
	if !strings.Contains(logValue, "category=origin") || !strings.Contains(logValue, "request_id=") {
		t.Fatalf("rejection log lacks safe classification: %s", logValue)
	}
	for _, forbidden := range []string{
		"untrusted.example", "internal.example", management.publicCSRF,
	} {
		if strings.Contains(logValue, forbidden) {
			t.Fatalf("rejection log exposed %q: %s", forbidden, logValue)
		}
	}
}

func TestManagementAcceptsAllowlistedPublicOriginWithInternalHost(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	management, err := New(store, runtime, adminauth.NewSessions(),
		adminauth.NewTransportPolicy(false, true, "https://api.example.com"),
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	management.RegisterRoutes(mux)
	request := httptest.NewRequest(http.MethodPost,
		"http://mt-server:8080/admin/api/v1/setup", strings.NewReader(`{}`))
	request.Host = "mt-server:8080"
	request.Header.Set("Origin", "https://api.example.com")
	request.Header.Set("X-CSRF-Token", management.publicCSRF)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_password") {
		t.Fatalf("allowlisted public origin was rejected: %d %s", recorder.Code, recorder.Body.String())
	}

	contents, err := json.Marshal(validSetupRequest())
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost,
		"http://mt-server:8080/admin/api/v1/setup", bytes.NewReader(contents))
	request.Host = "mt-server:8080"
	request.Header.Set("Origin", "https://API.EXAMPLE.COM:443")
	request.Header.Set("X-CSRF-Token", management.publicCSRF)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("proxy-style setup failed: %d %s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("proxy session cookie is not secure: %#v", cookies)
	}
}

func TestManagementReportsDurabilityWarning(t *testing.T) {
	handler, store, runtime, publicCSRF := newTestHandler(t)
	setup := performSetup(t, handler, publicCSRF)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", setup.Code, setup.Body.String())
	}
	cookie := setup.Result().Cookies()[0]
	var setupBody map[string]any
	if err := json.Unmarshal(setup.Body.Bytes(), &setupBody); err != nil {
		t.Fatal(err)
	}
	store.SyncDirectoryForTest(func(string) error { return errors.New("directory sync unavailable") })
	recorder := authenticatedRequest(t, handler, http.MethodPost,
		"/admin/api/v1/device-tokens", tokenRequest{Name: "Warning device"}, cookie,
		setupBody["csrf_token"].(string))
	if recorder.Code != http.StatusCreated || recorder.Header().Get("X-MT-State-Warning") != stateWarningValue {
		t.Fatalf("durability warning missing: %d %q %s", recorder.Code,
			recorder.Header().Get("X-MT-State-Warning"), recorder.Body.String())
	}
	if len(runtime.tokens) != 2 {
		t.Fatalf("committed token snapshot was not activated: %#v", runtime.tokens)
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

func TestQWeatherManagementTestConcurrencyAndRateLimits(t *testing.T) {
	handler, _, runtime, publicCSRF := newTestHandler(t)
	setup := performSetup(t, handler, publicCSRF)
	var setupBody map[string]any
	if err := json.Unmarshal(setup.Body.Bytes(), &setupBody); err != nil {
		t.Fatal(err)
	}
	cookie := setup.Result().Cookies()[0]
	csrf := setupBody["csrf_token"].(string)
	input := qweatherInput{
		APIHost: "account.re.qweatherapi.com", ProjectID: "project",
		CredentialID: "credential", TestLocation: validTestLocation(),
	}

	runtime.testStarted = make(chan struct{}, 1)
	runtime.testRelease = make(chan struct{})
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- authenticatedRequest(t, handler, http.MethodPost,
			"/admin/api/v1/settings/qweather/test", input, cookie, csrf)
	}()
	select {
	case <-runtime.testStarted:
	case <-time.After(time.Second):
		t.Fatal("first management test did not start")
	}
	busy := authenticatedRequest(t, handler, http.MethodPost,
		"/admin/api/v1/settings/qweather/test", input, cookie, csrf)
	if busy.Code != http.StatusTooManyRequests || !strings.Contains(busy.Body.String(), "qweather_test_busy") {
		t.Fatalf("concurrent management test returned %d %s", busy.Code, busy.Body.String())
	}
	close(runtime.testRelease)
	if result := <-first; result.Code != http.StatusOK {
		t.Fatalf("first management test failed: %d %s", result.Code, result.Body.String())
	}
	runtime.testStarted = nil
	runtime.testRelease = nil

	// Setup and the successful test above consumed two of the six attempts.
	for index := 0; index < 4; index++ {
		result := authenticatedRequest(t, handler, http.MethodPost,
			"/admin/api/v1/settings/qweather/test", input, cookie, csrf)
		if result.Code != http.StatusOK {
			t.Fatalf("allowed management test %d returned %d %s", index, result.Code, result.Body.String())
		}
	}
	limited := authenticatedRequest(t, handler, http.MethodPost,
		"/admin/api/v1/settings/qweather/test", input, cookie, csrf)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "60" ||
		!strings.Contains(limited.Body.String(), "qweather_test_rate_limited") {
		t.Fatalf("rate limit returned %d %v %s", limited.Code, limited.Header(), limited.Body.String())
	}
}

func TestManagementSessionSettingsAndPasswordFlow(t *testing.T) {
	handler, _, _, publicCSRF := newTestHandler(t)
	setup := performSetup(t, handler, publicCSRF)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", setup.Code, setup.Body.String())
	}
	var setupBody map[string]any
	if err := json.Unmarshal(setup.Body.Bytes(), &setupBody); err != nil {
		t.Fatal(err)
	}
	setupCookie := setup.Result().Cookies()[0]
	setupCSRF := setupBody["csrf_token"].(string)

	status := plainRequest(handler, http.MethodGet, "/admin/api/v1/status", nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"state_durability":"confirmed"`) {
		t.Fatalf("unexpected status response: %d %s", status.Code, status.Body.String())
	}
	session := authenticatedRequest(t, handler, http.MethodGet,
		"/admin/api/v1/session", nil, setupCookie, setupCSRF)
	if session.Code != http.StatusOK {
		t.Fatalf("session lookup failed: %d %s", session.Code, session.Body.String())
	}

	input := qweatherInput{
		APIHost: "next.re.qweatherapi.com", ProjectID: "project-next",
		CredentialID: "credential-next", TestLocation: validTestLocation(),
	}
	tested := authenticatedRequest(t, handler, http.MethodPost,
		"/admin/api/v1/settings/qweather/test", input, setupCookie, setupCSRF)
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"status":"ok"`) {
		t.Fatalf("QWeather test failed: %d %s", tested.Code, tested.Body.String())
	}
	saved := authenticatedRequest(t, handler, http.MethodPut,
		"/admin/api/v1/settings/qweather", input, setupCookie, setupCSRF)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"status":"saved"`) {
		t.Fatalf("QWeather update failed: %d %s", saved.Code, saved.Body.String())
	}

	badLogin := publicRequest(t, handler, http.MethodPost, "/admin/api/v1/session",
		loginRequest{Password: "wrong password"}, publicCSRF)
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login returned %d %s", badLogin.Code, badLogin.Body.String())
	}
	login := publicRequest(t, handler, http.MethodPost, "/admin/api/v1/session",
		loginRequest{Password: "correct horse battery staple"}, publicCSRF)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	var loginBody map[string]string
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	loginCookie := login.Result().Cookies()[0]
	if staleCSRF := authenticatedRequest(t, handler, http.MethodPost,
		"/admin/api/v1/device-tokens", tokenRequest{Name: "stale"}, loginCookie, setupCSRF); staleCSRF.Code != http.StatusForbidden || !strings.Contains(staleCSRF.Body.String(), "csrf_rejected") {
		t.Fatalf("stale CSRF was accepted: %d %s", staleCSRF.Code, staleCSRF.Body.String())
	}

	wrongPassword := authenticatedRequest(t, handler, http.MethodPut,
		"/admin/api/v1/account/password", passwordRequest{
			CurrentPassword: "wrong password", NewPassword: "replacement password value",
		}, loginCookie, loginBody["csrf_token"])
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password returned %d", wrongPassword.Code)
	}
	changed := authenticatedRequest(t, handler, http.MethodPut,
		"/admin/api/v1/account/password", passwordRequest{
			CurrentPassword: "correct horse battery staple", NewPassword: "replacement password value",
		}, loginCookie, loginBody["csrf_token"])
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), "password_updated") {
		t.Fatalf("password change failed: %d %s", changed.Code, changed.Body.String())
	}
	if after := authenticatedRequest(t, handler, http.MethodGet,
		"/admin/api/v1/session", nil, loginCookie, loginBody["csrf_token"]); after.Code != http.StatusUnauthorized {
		t.Fatalf("password change did not invalidate session: %d", after.Code)
	}
}

func TestManagementAssetsAreServedOnlyAtExactPaths(t *testing.T) {
	handler, _, _, _ := newTestHandler(t)
	for _, path := range []string{"/admin/", "/admin/assets/styles.css", "/admin/assets/app.js"} {
		recorder := plainRequest(handler, http.MethodGet, path, nil)
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("asset %s returned %d with headers %v", path, recorder.Code, recorder.Header())
		}
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

func publicRequest(t *testing.T, handler http.Handler, method, path string,
	body any, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	contents, _ := json.Marshal(body)
	request := httptest.NewRequest(method, "http://api.example.com"+path, bytes.NewReader(contents))
	request.Header.Set("Origin", "http://api.example.com")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func plainRequest(handler http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, "http://api.example.com"+path, body))
	return recorder
}

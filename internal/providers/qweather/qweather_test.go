package qweather

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

const currentFixture = `{
  "code":"200",
  "updateTime":"2026-08-02T10:00+08:00",
  "now":{
    "obsTime":"2026-08-02T09:50+08:00",
    "temp":"28","feelsLike":"31","icon":"101","text":"多云",
    "wind360":"135","windDir":"东南风","windScale":"2","windSpeed":"8",
    "humidity":"72","precip":"0.0","pressure":"1004","vis":"16",
    "cloud":"80","dew":"22"
  }
}`

const hourlyFixture = `{
  "code":"200",
  "updateTime":"2026-08-02T10:00+08:00",
  "hourly":[{
    "fxTime":"2026-08-02T11:00+08:00","temp":"29","icon":"101","text":"多云",
    "wind360":"140","windDir":"东南风","windScale":"2","windSpeed":"9",
    "humidity":"70","pop":"20","precip":"0.0","pressure":"1003",
    "cloud":"75","dew":"22"
  }]
}`

const dailyFixture = `{
  "code":"200",
  "updateTime":"2026-08-02T10:00+08:00",
  "daily":[{
    "fxDate":"2026-08-02","sunrise":"05:20","sunset":"18:55",
    "moonrise":"20:10","moonset":"06:30","moonPhase":"盈凸月","moonPhaseIcon":"803",
    "tempMax":"32","tempMin":"25","iconDay":"101","textDay":"多云",
    "iconNight":"150","textNight":"晴","wind360Day":"135","windDirDay":"东南风",
    "windScaleDay":"2","windSpeedDay":"9","wind360Night":"90","windDirNight":"东风",
    "windScaleNight":"2","windSpeedNight":"7","humidity":"74","precip":"0.0",
    "pressure":"1004","vis":"18","cloud":"60","uvIndex":"6"
  }]
}`

const alertsFixture = `{
  "code":"200",
  "updateTime":"2026-08-02T10:00+08:00",
  "fxLink":"https://www.qweather.com/severe-weather/example.html",
  "warning":[{
    "id":"warning-active","sender":"示例气象台",
    "pubTime":"2026-08-02T09:30+08:00","title":"高温橙色预警",
    "startTime":"2026-08-02T09:30+08:00","endTime":"2026-08-02T18:00+08:00",
    "status":"预警中","level":"橙色","severity":"Severe",
    "type":"11B09","typeName":"高温","urgency":"Immediate","certainty":"Likely",
    "text":"预计白天气温较高。","instruction":"减少户外活动。"
  }]
}`

func TestSignerProducesValidEdDSAJWT(t *testing.T) {
	privateKeyPEM, publicKey := testPrivateKey(t)
	value, err := newSigner(privateKeyPEM, "credential", "project")
	if err != nil {
		t.Fatal(err)
	}
	fixedTime := time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC)
	value.now = func() time.Time { return fixedTime }
	token, err := value.token()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT shape %q", token)
	}
	if !ed25519.Verify(publicKey, []byte(parts[0]+"."+parts[1]), mustDecode(t, parts[2])) {
		t.Fatal("invalid Ed25519 signature")
	}
	var header map[string]any
	if err := json.Unmarshal(mustDecode(t, parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "EdDSA" || header["kid"] != "credential" {
		t.Fatalf("unexpected header %#v", header)
	}
	var claims map[string]any
	if err := json.Unmarshal(mustDecode(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["sub"] != "project" || int64(claims["iat"].(float64)) != fixedTime.Add(-30*time.Second).Unix() ||
		int64(claims["exp"].(float64)) != fixedTime.Add(5*time.Minute).Unix() {
		t.Fatalf("unexpected claims %#v", claims)
	}
}

func TestPublicKeyFingerprintAndProviderMetadata(t *testing.T) {
	privateKeyPEM, publicKey := testPrivateKey(t)
	fingerprint, err := PublicKeyFingerprint(privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(publicKey)
	if fingerprint != base64.RawURLEncoding.EncodeToString(expected[:]) {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}
	if _, err := PublicKeyFingerprint([]byte("not PEM")); err == nil {
		t.Fatal("invalid key was accepted")
	}
	client := testClient(t, "http://api.example.com", privateKeyPEM)
	if source := client.Source(); source.ID != "qweather" || source.AttributionURL == "" {
		t.Fatalf("unexpected source %#v", source)
	}
	upstream := &UpstreamError{HTTPStatus: http.StatusBadRequest, Delay: time.Minute}
	if upstream.Error() != "qweather returned HTTP status 400" || upstream.RetryDelay() != time.Minute {
		t.Fatalf("unexpected upstream error behavior: %s %s", upstream, upstream.RetryDelay())
	}
	upstream.Code = "429"
	if upstream.Error() != "qweather returned code 429" {
		t.Fatalf("unexpected provider-code error %q", upstream.Error())
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	if _, err := New("not-a-url", privateKeyPEM, "credential", "project", "zh", "m",
		time.Second, time.Minute); err == nil {
		t.Fatal("invalid base URL was accepted")
	}
	if _, err := New("https://api.example.com", []byte("invalid"), "credential", "project", "zh", "m",
		time.Second, time.Minute); err == nil {
		t.Fatal("invalid private key was accepted")
	}
}

func TestClientFetchesAndNormalizesAllDatasets(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	responses := map[string]string{
		"/v7/weather/now": currentFixture,
		"/v7/weather/24h": hourlyFixture,
		"/v7/weather/7d":  dailyFixture,
		"/v7/warning/now": alertsFixture,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing bearer token")
		}
		if r.URL.Query().Get("location") != "120.10,30.20" ||
			r.URL.Query().Get("lang") != "zh" || r.URL.Query().Get("unit") != "m" {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	point := location.Point{Latitude: 30.2, Longitude: 120.1}

	current, err := client.Fetch(context.Background(), weather.KindCurrent, point)
	if err != nil {
		t.Fatal(err)
	}
	if current.Data.(weather.Current).TemperatureC != 28 {
		t.Fatalf("unexpected current data %#v", current.Data)
	}
	hourly, err := client.Fetch(context.Background(), weather.KindHourly, point)
	if err != nil {
		t.Fatal(err)
	}
	if len(hourly.Data.(weather.Hourly).Hours) != 1 {
		t.Fatalf("unexpected hourly data %#v", hourly.Data)
	}
	daily, err := client.Fetch(context.Background(), weather.KindDaily, point)
	if err != nil {
		t.Fatal(err)
	}
	day := daily.Data.(weather.Daily).Days[0]
	if day.Sunrise == nil {
		t.Fatal("daily sunrise is missing")
	}
	_, offset := day.Sunrise.Zone()
	if offset != 8*60*60 {
		t.Fatalf("daily local offset was not preserved: %#v", day.Sunrise)
	}
	alerts, err := client.Fetch(context.Background(), weather.KindAlerts, point)
	if err != nil {
		t.Fatal(err)
	}
	alertData := alerts.Data.(weather.Alerts)
	if len(alertData.Items) != 1 || alertData.Items[0].Severity != "severe" ||
		alertData.Items[0].Status != "active" || alertData.Items[0].StartsAt == nil ||
		alertData.DetailURL == "" {
		t.Fatalf("unexpected alerts data %#v", alertData)
	}
}

func TestParseAlertsSupportsEmptyAndBoundsPublicContent(t *testing.T) {
	empty, err := parseAlerts([]byte(`{
      "updateTime":"2026-08-02T10:00+08:00","warning":[]
    }`))
	if err != nil || len(empty.Data.(weather.Alerts).Items) != 0 {
		t.Fatalf("unexpected empty alerts result %#v %v", empty, err)
	}
	longText := strings.Repeat("预", maximumDescriptionRunes+1)
	fixture := strings.Replace(alertsFixture, "预计白天气温较高。", longText, 1)
	fixture = strings.Replace(fixture, "https://www.qweather.com/severe-weather/example.html",
		"https://malicious.example/alert", 1)
	result, err := parseAlerts([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	data := result.Data.(weather.Alerts)
	if data.DetailURL != "" || !data.Items[0].ContentTruncated ||
		len([]rune(data.Items[0].Description)) != maximumDescriptionRunes {
		t.Fatalf("alert bounds were not applied %#v", data)
	}
}

func TestParseAlertsSortsBeforeTruncating(t *testing.T) {
	warnings := []map[string]string{
		{"id": "active-minor", "title": "active minor", "type": "minor", "typeName": "Minor",
			"pubTime": "2026-08-02T10:00+08:00", "status": "active", "severity": "minor"},
		{"id": "active-extreme", "title": "active extreme", "type": "extreme", "typeName": "Extreme",
			"pubTime": "2026-08-02T09:00+08:00", "status": "active", "severity": "extreme"},
		{"id": "unknown-extreme", "title": "unknown extreme", "type": "unknown", "typeName": "Unknown",
			"pubTime": "2026-08-02T11:00+08:00", "status": "other", "severity": "extreme"},
		{"id": "cancelled-extreme", "title": "cancelled extreme", "type": "cancelled", "typeName": "Cancelled",
			"pubTime": "2026-08-02T12:00+08:00", "status": "cancelled", "severity": "extreme"},
	}
	for index := 0; index < 29; index++ {
		warnings = append(warnings, map[string]string{
			"id": fmt.Sprintf("filler-%02d", index), "title": "filler", "type": "filler",
			"typeName": "Filler", "pubTime": "2026-08-02T08:00+08:00",
			"status": "cancelled", "severity": "minor",
		})
	}
	body, err := json.Marshal(map[string]any{
		"updateTime": "2026-08-02T10:00+08:00",
		"warning":    warnings,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := parseAlerts(body)
	if err != nil {
		t.Fatal(err)
	}
	alerts := result.Data.(weather.Alerts)
	if !alerts.Truncated || len(alerts.Items) != maximumAlerts {
		t.Fatalf("unexpected alert truncation %#v", alerts)
	}
	want := []string{"active-extreme", "active-minor", "unknown-extreme", "cancelled-extreme"}
	for index, id := range want {
		if alerts.Items[index].ID != id {
			t.Fatalf("alert %d = %q, want %q", index, alerts.Items[index].ID, id)
		}
	}
	for _, alert := range alerts.Items {
		if alert.ID == "filler-28" {
			t.Fatal("lowest-priority alert was retained after truncation")
		}
	}
}

func TestClientRetriesOneServerFailure(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(currentFixture))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	client.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two calls, got %d", calls.Load())
	}
}

func TestClientRetriesOneProviderServerCode(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"code":"500"}`))
			return
		}
		_, _ = w.Write([]byte(currentFixture))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	client.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two calls, got %d", calls.Load())
	}
}

func TestClientRetriesOneTimeoutAndPreservesCause(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	client := testClient(t, "http://api.example.com", privateKeyPEM)
	var calls atomic.Int32
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, context.DeadlineExceeded
	})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout cause was not preserved: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two calls, got %d", calls.Load())
	}
}

func TestClientSanitizesNetworkErrors(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	client := testClient(t, "http://api.example.com", privateKeyPEM)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed for https://private.example/location=120.1,30.2")
	})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
	if err == nil {
		t.Fatal("expected network error")
	}
	for _, forbidden := range []string{"private.example", "location=", "120.1"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("network error leaked %q: %v", forbidden, err)
		}
	}
}

func TestClientOpensCircuitOnAuthenticationFailure(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	fixedTime := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return fixedTime }
	if _, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{}); err == nil {
		t.Fatal("expected authentication failure")
	}
	if !errors.Is(client.Ready(), ErrCircuitOpen) {
		t.Fatalf("expected open circuit, got %v", client.Ready())
	}
	diagnostics := client.Diagnostics()
	if diagnostics.Status != "blocked" || diagnostics.BlockedUntil == nil ||
		!diagnostics.BlockedUntil.Equal(fixedTime.Add(15*time.Minute)) {
		t.Fatalf("unexpected blocked diagnostics %#v", diagnostics)
	}
	fixedTime = fixedTime.Add(15 * time.Minute)
	if diagnostics = client.Diagnostics(); diagnostics.Status != "ready" || diagnostics.BlockedUntil != nil {
		t.Fatalf("unexpected recovered diagnostics %#v", diagnostics)
	}
}

func TestClientOpensCircuitOnProviderAuthenticationCode(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"403"}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	fixedTime := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return fixedTime }
	if _, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{}); err == nil {
		t.Fatal("expected authentication failure")
	}
	if !errors.Is(client.Ready(), ErrCircuitOpen) {
		t.Fatalf("expected open circuit, got %v", client.Ready())
	}
}

func TestClientHonorsRateLimitAndResponseBound(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	t.Run("rate limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		client := testClient(t, server.URL, privateKeyPEM)
		_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
		var upstreamError *UpstreamError
		if !errors.As(err, &upstreamError) || upstreamError.Delay != 2*time.Minute {
			t.Fatalf("unexpected error %#v", err)
		}
		if upstreamError.Class != ErrorRateLimit || !errors.Is(client.Ready(), ErrCircuitOpen) {
			t.Fatalf("rate limit did not open account block: %#v", upstreamError)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxResponseSize+1)))
		}))
		defer server.Close()
		client := testClient(t, server.URL, privateKeyPEM)
		_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
		if err == nil || !strings.Contains(err.Error(), "1 MiB") {
			t.Fatalf("unexpected error %v", err)
		}
	})
}

func TestClientDoesNotGloballyBlockBadRequest(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Class != ErrorBadRequest ||
		upstream.Delay != 15*time.Minute {
		t.Fatalf("unexpected bad-request classification: %#v", err)
	}
	if errors.Is(client.Ready(), ErrCircuitOpen) {
		t.Fatal("bad request opened account circuit")
	}
}

func TestClientBusinessRateLimitUsesDefaultAccountBlock(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"429"}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Class != ErrorRateLimit || upstream.Delay != time.Minute {
		t.Fatalf("unexpected business rate limit: %#v", err)
	}
	if !errors.Is(client.Ready(), ErrCircuitOpen) {
		t.Fatalf("business rate limit did not block account: %v", client.Ready())
	}
}

func TestClientBusinessRateLimitHonorsRetryAfter(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		_, _ = w.Write([]byte(`{"code":"429"}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Class != ErrorRateLimit || upstream.Delay != 2*time.Minute {
		t.Fatalf("unexpected business rate limit: %#v", err)
	}
}

func TestClientLimitsActiveUpstreamConcurrency(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, maximumConcurrency)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		_, _ = w.Write([]byte(currentFixture))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	const callers = maximumConcurrency + 4
	results := make(chan error, callers)
	for range callers {
		go func() {
			_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
			results <- err
		}()
	}
	for range maximumConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("upstream concurrency did not reach configured capacity")
		}
	}
	if got := maximum.Load(); got != maximumConcurrency {
		t.Fatalf("unexpected active concurrency %d", got)
	}
	close(release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientSemaphoreWaitHonorsContextCancellation(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	started := make(chan struct{}, maximumConcurrency)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(currentFixture))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)
	for range maximumConcurrency {
		go func() { _, _ = client.Fetch(context.Background(), weather.KindCurrent, location.Point{}) }()
	}
	for range maximumConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("upstream requests did not start")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Fetch(ctx, weather.KindCurrent, location.Point{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled semaphore wait returned %v", err)
	}
	close(release)
}

func TestClientCloseMarksProviderUnavailable(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	client := testClient(t, "http://api.example.com", privateKeyPEM)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(client.Ready(), ErrClosed) {
		t.Fatalf("closed client readiness = %v", client.Ready())
	}
}

func TestParsersRejectMalformedRequiredFields(t *testing.T) {
	for _, value := range []string{"invalid", "NaN", "+Inf", "-Inf"} {
		malformed := strings.Replace(currentFixture, `"temp":"28"`, `"temp":"`+value+`"`, 1)
		if _, err := parseCurrent([]byte(malformed)); err == nil {
			t.Fatalf("expected %q number to fail", value)
		}
	}
	badCode := strings.Replace(currentFixture, `"code":"200"`, `"code":"403"`, 1)
	var status commonResponse
	if err := json.Unmarshal([]byte(badCode), &status); err != nil || status.Code != "403" {
		t.Fatalf("embedded response status failed: %#v %v", status, err)
	}
}

func TestParsersRejectMalformedShapesAndAllowMissingOptionalValues(t *testing.T) {
	if _, err := parseCurrent([]byte("{")); err == nil {
		t.Fatal("malformed current JSON was accepted")
	}
	if _, err := parseHourly([]byte(`{"updateTime":"2026-08-02T10:00+08:00","hourly":[]}`)); err == nil {
		t.Fatal("empty hourly forecast was accepted")
	}
	if _, err := parseDaily([]byte(`{"updateTime":"2026-08-02T10:00+08:00","daily":[]}`)); err == nil {
		t.Fatal("empty daily forecast was accepted")
	}
	missingCondition := strings.Replace(currentFixture, `"icon":"101"`, `"icon":""`, 1)
	if _, err := parseCurrent([]byte(missingCondition)); err == nil {
		t.Fatal("missing current condition was accepted")
	}
	missingOptional := strings.Replace(strings.Replace(currentFixture,
		`"cloud":"80"`, `"cloud":""`, 1), `"dew":"22"`, `"dew":""`, 1)
	result, err := parseCurrent([]byte(missingOptional))
	if err != nil {
		t.Fatal(err)
	}
	current := result.Data.(weather.Current)
	if current.CloudPercent != nil || current.DewPointC != nil {
		t.Fatalf("missing optional values were not preserved: %#v", current)
	}
	if _, err := parseTime("invalid", "time"); err == nil {
		t.Fatal("invalid timestamp was accepted")
	}
	if value, err := parseClock("2026-08-02", "", time.Now(), "clock"); err != nil || value != nil {
		t.Fatalf("empty clock returned %#v %v", value, err)
	}
	if _, err := parseClock("2026-08-02", "invalid", time.Now(), "clock"); err == nil {
		t.Fatal("invalid clock was accepted")
	}
}

func testClient(t *testing.T, baseURL string, privateKeyPEM []byte) *Client {
	t.Helper()
	client, err := New(baseURL, privateKeyPEM, "credential", "project", "zh", "m",
		2*time.Second, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testPrivateKey(t *testing.T) ([]byte, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), publicKey
}

func mustDecode(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestRetryAfterCapsValues(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	if got := retryAfter(strconv.Itoa(3600), now); got != 15*time.Minute {
		t.Fatalf("unexpected cap %s", got)
	}
	if got := retryAfter("", now); got != time.Minute {
		t.Fatalf("unexpected default %s", got)
	}
	if got := retryAfter(now.Add(2*time.Minute).Format(http.TimeFormat), now); got != 2*time.Minute {
		t.Fatalf("unexpected HTTP-date delay %s", got)
	}
}

func TestSleepContextCompletesAndCancels(t *testing.T) {
	if err := sleepContext(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sleep returned %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

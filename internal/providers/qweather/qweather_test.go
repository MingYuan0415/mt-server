package qweather

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
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

func TestClientFetchesAndNormalizesAllDatasets(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	responses := map[string]string{
		"/v7/weather/now": currentFixture,
		"/v7/weather/24h": hourlyFixture,
		"/v7/weather/7d":  dailyFixture,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing bearer token")
		}
		if r.URL.Query().Get("location") != "120.1,30.2" ||
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
		if !errors.As(err, &upstreamError) || upstreamError.RetryAfter != 2*time.Minute {
			t.Fatalf("unexpected error %#v", err)
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
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

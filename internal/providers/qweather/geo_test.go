package qweather

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

func TestLocalizeQueriesCityLookupWithGridCoordinates(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/geo/v2/city/lookup" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing bearer token")
		}
		if r.URL.Query().Get("location") != "114.1,22.5" ||
			r.URL.Query().Get("number") != "1" ||
			r.URL.Query().Get("lang") != "zh" {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		if _, present := r.URL.Query()["unit"]; present {
			t.Errorf("geo lookup must not carry the weather unit parameter: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code":"200",
			"location":[{"name":"东城","adm1":"北京市","tz":"Asia/Shanghai"}]
		}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)

	metadata, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.City != "东城" || metadata.Region != "北京市" || metadata.Timezone != "Asia/Shanghai" {
		t.Fatalf("unexpected localized metadata %#v", metadata)
	}
}

func TestLocalizeRejectsEmptyLocationResult(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"200","location":[]}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)

	if _, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1}); err == nil {
		t.Fatal("empty geo result was accepted")
	}
}

func TestLocalizeCredentialFailureDoesNotOpenWeatherCircuit(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http 401", status: http.StatusUnauthorized},
		{name: "http 403", status: http.StatusForbidden},
		{name: "body 401", body: `{"code":"401"}`},
		{name: "body 403", body: `{"code":"403"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			privateKeyPEM, _ := testPrivateKey(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/geo/v2/city/lookup" {
					http.NotFound(w, r)
					return
				}
				if test.status != 0 {
					w.WriteHeader(test.status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := testClient(t, server.URL, privateKeyPEM)

			if _, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1}); err == nil {
				t.Fatal("geo credential failure was accepted")
			}
			if err := client.Ready(); err != nil {
				t.Fatalf("geo credential failure must not open the weather circuit: %v", err)
			}
		})
	}
}

func TestLocalizeRateLimitDoesNotOpenWeatherCircuit(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "http 429"},
		{name: "body 429", body: `{"code":"429"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			privateKeyPEM, _ := testPrivateKey(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/geo/v2/city/lookup" {
					if test.body == "" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(test.body))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(currentFixture))
			}))
			defer server.Close()
			client := testClient(t, server.URL, privateKeyPEM)

			if _, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1}); err == nil {
				t.Fatal("geo rate limit was accepted")
			}
			if err := client.Ready(); err != nil {
				t.Fatalf("geo rate limit must not open the weather circuit: %v", err)
			}
			if _, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{Latitude: 22.5, Longitude: 114.1}); err != nil {
				t.Fatalf("weather must remain available after a geo rate limit: %v", err)
			}
		})
	}
}

func TestLocalizeSanitizesBodyReadErrors(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	client := testClient(t, "http://api.example.com", privateKeyPEM)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(&failingReader{
				err: errors.New("read failed at https://private.example/geo/v2/city/lookup?location=120.1,30.2"),
			}),
			Header: make(http.Header),
		}, nil
	})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1})
	if err == nil {
		t.Fatal("expected body read error")
	}
	for _, forbidden := range []string{"private.example", "geo/v2", "location=", "120.1", "30.2"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("body read error leaked %q: %v", forbidden, err)
		}
	}
}

type failingReader struct {
	err error
}

func (f *failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestLocalizeRejectsNoUsableNames(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"200","location":[{}]}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)

	if _, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1}); err == nil {
		t.Fatal("geo result without usable names was accepted")
	}
}

func TestLocalizeRejectsInvalidOnlyNames(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"200","location":[{"name":"bad\tname","adm1":"","tz":""}]}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)

	if _, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1}); err == nil {
		t.Fatal("geo result with only invalid names was accepted")
	}
}

func TestLocalizeDropsInvalidFieldsIndividually(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code":"200",
			"location":[{"name":"东城","adm1":"bad\tadm1","tz":""}]
		}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)

	metadata, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.City != "东城" || metadata.Region != "" || metadata.Timezone != "" {
		t.Fatalf("invalid fields must be cleared individually: %#v", metadata)
	}
}

func TestLocalizeNameValidation(t *testing.T) {
	withinBoundary := strings.Repeat("深", 128)
	withPadding := " " + strings.Repeat("深", 128) + " "
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "trimmed boundary", value: withPadding, valid: true},
		{name: "overlong", value: strings.Repeat("深", 129), valid: false},
		{name: "control character", value: "bad\tname", valid: false},
		{name: "invalid utf8", value: "bad\xffname", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if value, ok := validLocalizedName(test.value); ok != test.valid {
				t.Fatalf("validLocalizedName(%q) = %q, %v; want valid=%v",
					test.value, value, ok, test.valid)
			}
		})
	}
	if value, ok := validLocalizedName(withinBoundary); !ok || value != withinBoundary {
		t.Fatalf("boundary value must stay valid: %q, %v", value, ok)
	}
	if value, ok := validLocalizedName(withPadding); !ok || value != withinBoundary {
		t.Fatalf("padding must be trimmed before counting: %q, %v", value, ok)
	}
}

func TestLocalizeRateLimitReportsRetryDelay(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)

	_, err := client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Class != ErrorRateLimit ||
		upstream.Delay != 90*time.Second {
		t.Fatalf("geo rate limit must carry the Retry-After delay: %#v", err)
	}
}

func TestClientPreservesDeadlineExceededWhileReadingBody(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	client := testClient(t, "http://api.example.com", privateKeyPEM)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(&failingReader{
				err: fmt.Errorf("read failed: %w", context.DeadlineExceeded),
			}),
			Header: make(http.Header),
		}, nil
	})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := client.Fetch(context.Background(), weather.KindCurrent, location.Point{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("body-read timeout must preserve DeadlineExceeded: %v", err)
	}
	for _, forbidden := range []string{"api.example.com", "location=", "120.1"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("timeout error leaked %q: %v", forbidden, err)
		}
	}
}

func TestLocalizeSharesProviderConcurrencyLimit(t *testing.T) {
	privateKeyPEM, _ := testPrivateKey(t)
	started := make(chan struct{}, maximumConcurrency)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/geo/v2/city/lookup" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"200","location":[{"name":"东城"}]}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, privateKeyPEM)

	done := make(chan struct{}, maximumConcurrency)
	for range maximumConcurrency {
		go func() {
			_, _ = client.Localize(context.Background(), location.Point{Latitude: 22.5, Longitude: 114.1})
			done <- struct{}{}
		}()
	}
	for range maximumConcurrency {
		<-started
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := client.Fetch(ctx, weather.KindCurrent, location.Point{Latitude: 22.5, Longitude: 114.1}); err == nil {
		t.Fatal("weather fetch must wait for the shared concurrency slot")
	}
	close(release)
	for range maximumConcurrency {
		<-done
	}
}

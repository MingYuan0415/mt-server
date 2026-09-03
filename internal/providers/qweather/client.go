// Package qweather adapts QWeather responses to the weather domain.
package qweather

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

const (
	maxResponseSize       = 1024 * 1024
	retryBaseDelay        = 200 * time.Millisecond
	maximumConcurrency    = 8
	badRequestRetryDelay  = 15 * time.Minute
	defaultRateLimitDelay = time.Minute
)

// ErrCircuitOpen means authentication or configuration failures are cooling down.
var ErrCircuitOpen = errors.New("qweather configuration circuit is open")

// ErrClosed means the provider lifecycle has ended.
var ErrClosed = errors.New("qweather client is closed")

// ErrorClass identifies the safe operational category of an upstream failure.
type ErrorClass string

const (
	ErrorBadRequest  ErrorClass = "bad_request"
	ErrorCredentials ErrorClass = "credentials"
	ErrorRateLimit   ErrorClass = "rate_limit"
	ErrorServer      ErrorClass = "server"
	ErrorResponse    ErrorClass = "response"
)

// UpstreamError describes a safe-to-log provider failure.
type UpstreamError struct {
	HTTPStatus int
	Code       string
	Class      ErrorClass
	Delay      time.Duration
}

func (e *UpstreamError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("qweather returned code %s", e.Code)
	}
	return fmt.Sprintf("qweather returned HTTP status %d", e.HTTPStatus)
}

// RetryDelay exposes a provider-request cooldown without leaking response data.
func (e *UpstreamError) RetryDelay() time.Duration {
	return e.Delay
}

// DiagnosticClass exposes only the bounded operational category.
func (e *UpstreamError) DiagnosticClass() string { return string(e.Class) }

// Client calls one account-specific QWeather API host.
type Client struct {
	baseURL         *url.URL
	httpClient      *http.Client
	signer          *signer
	language        string
	unit            string
	circuitCooldown time.Duration
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error
	slots           chan struct{}

	circuitMu    sync.Mutex
	blockedUntil time.Time
	closed       bool
}

// Source returns the device-facing QWeather attribution.
func (c *Client) Source() weather.Source {
	return weather.Source{
		ID:             "qweather",
		Name:           "QWeather",
		AttributionURL: "https://www.qweather.com/",
	}
}

// New constructs a QWeather provider client.
func New(baseURL string, privateKeyPEM []byte, credentialID, projectID,
	language, unit string, requestTimeout, circuitCooldown time.Duration) (*Client, error) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" ||
		parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, errors.New("invalid QWeather base URL")
	}
	providerSigner, err := newSigner(privateKeyPEM, credentialID, projectID)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = requestTimeout
	return &Client{
		baseURL: parsedURL,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: transport,
		},
		signer:          providerSigner,
		language:        language,
		unit:            unit,
		circuitCooldown: circuitCooldown,
		now:             time.Now,
		sleep:           sleepContext,
		slots:           make(chan struct{}, maximumConcurrency),
	}, nil
}

// Ready reports whether a configuration circuit is currently open.
func (c *Client) Ready() error {
	c.circuitMu.Lock()
	defer c.circuitMu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if c.now().Before(c.blockedUntil) {
		return ErrCircuitOpen
	}
	return nil
}

// Diagnostics returns circuit state without making an upstream request.
func (c *Client) Diagnostics() weather.ProviderDiagnostics {
	c.circuitMu.Lock()
	defer c.circuitMu.Unlock()
	if c.closed {
		return weather.ProviderDiagnostics{Status: "closed"}
	}
	if c.now().Before(c.blockedUntil) {
		until := c.blockedUntil.UTC()
		return weather.ProviderDiagnostics{Status: "blocked", BlockedUntil: &until}
	}
	return weather.ProviderDiagnostics{Status: "ready"}
}

// Fetch obtains one normalized weather dataset.
func (c *Client) Fetch(ctx context.Context, kind weather.Kind,
	point location.Point) (weather.ProviderResult, error) {
	var path string
	switch kind {
	case weather.KindCurrent:
		path = "/v7/weather/now"
	case weather.KindHourly:
		path = "/v7/weather/24h"
	case weather.KindDaily:
		path = "/v7/weather/7d"
	case weather.KindAlerts:
		path = "/v7/warning/now"
	default:
		return weather.ProviderResult{}, fmt.Errorf("unsupported weather kind %q", kind)
	}
	if err := c.acquire(ctx); err != nil {
		return weather.ProviderResult{}, err
	}
	defer func() { <-c.slots }()

	body, err := c.request(ctx, path, point)
	if err != nil {
		return weather.ProviderResult{}, err
	}
	switch kind {
	case weather.KindCurrent:
		return parseCurrent(body)
	case weather.KindHourly:
		return parseHourly(body)
	case weather.KindDaily:
		return parseDaily(body)
	case weather.KindAlerts:
		return parseAlerts(body)
	default:
		return weather.ProviderResult{}, fmt.Errorf("unsupported weather kind %q", kind)
	}
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close ends the provider lifecycle and closes idle upstream connections.
func (c *Client) Close() error {
	c.circuitMu.Lock()
	c.closed = true
	c.circuitMu.Unlock()
	c.httpClient.CloseIdleConnections()
	return nil
}

func (c *Client) request(ctx context.Context, path string,
	point location.Point) ([]byte, error) {
	query := url.Values{}
	query.Set("location", strconv.FormatFloat(point.Longitude, 'f', 2, 64)+","+
		strconv.FormatFloat(point.Latitude, 'f', 2, 64))
	query.Set("lang", c.language)
	query.Set("unit", c.unit)
	return c.requestWithQuery(ctx, path, query, true)
}

// requestWithQuery performs a signed GET against the account-specific API
// host with an explicit query, retrying once on network and server errors.
// circuitOnFailure allows credentials and rate-limit failures to open the
// shared circuit so weather readiness reflects account-level problems;
// best-effort lookups pass false so that an endpoint-level rejection (for
// example an account without GeoAPI, or GeoAPI rate limiting) cannot poison
// weather availability.
func (c *Client) requestWithQuery(ctx context.Context, path string,
	query url.Values, circuitOnFailure bool) ([]byte, error) {
	if err := c.Ready(); err != nil {
		return nil, err
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawQuery = query.Encode()

	for attempt := 0; attempt < 2; attempt++ {
		body, retry, err := c.requestOnce(ctx, endpoint.String(), circuitOnFailure)
		if err == nil {
			return body, nil
		}
		if !retry || attempt == 1 {
			return nil, err
		}
		if err := c.sleep(ctx, retryDelay()); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("qweather request failed")
}

func (c *Client) requestOnce(ctx context.Context, endpoint string,
	circuitOnFailure bool) ([]byte, bool, error) {
	token, err := c.signer.token()
	if err != nil {
		return nil, false, fmt.Errorf("sign qweather request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "mt-server/1")

	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, true, fmt.Errorf("qweather request timed out: %w", context.DeadlineExceeded)
		}
		return nil, true, errors.New("qweather network request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, true, fmt.Errorf("qweather response timed out: %w", context.DeadlineExceeded)
		}
		return nil, true, errors.New("qweather response read failed")
	}
	if len(body) > maxResponseSize {
		return nil, false, errors.New("qweather response exceeds 1 MiB")
	}

	if response.StatusCode == http.StatusTooManyRequests {
		delay := retryAfter(response.Header.Get("Retry-After"), c.now())
		if circuitOnFailure {
			c.blockFor(delay)
		}
		return nil, false, &UpstreamError{HTTPStatus: response.StatusCode,
			Class: ErrorRateLimit, Delay: delay}
	}
	if response.StatusCode == http.StatusBadRequest {
		return nil, false, &UpstreamError{HTTPStatus: response.StatusCode,
			Class: ErrorBadRequest, Delay: badRequestRetryDelay}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		delay := c.circuitCooldown
		if circuitOnFailure {
			c.blockFor(delay)
		}
		return nil, false, &UpstreamError{HTTPStatus: response.StatusCode,
			Class: ErrorCredentials, Delay: delay}
	}
	if response.StatusCode >= 500 {
		return nil, true, &UpstreamError{HTTPStatus: response.StatusCode, Class: ErrorServer}
	}
	if response.StatusCode != http.StatusOK {
		return nil, false, &UpstreamError{HTTPStatus: response.StatusCode, Class: ErrorResponse}
	}

	var status commonResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, false, fmt.Errorf("decode qweather status: %w", err)
	}
	if status.Code != "200" {
		errorValue := &UpstreamError{Code: status.Code, Class: ErrorResponse}
		retry := false
		if status.Code == "400" {
			errorValue.Class = ErrorBadRequest
			errorValue.Delay = badRequestRetryDelay
		} else if status.Code == "401" || status.Code == "403" {
			errorValue.Class = ErrorCredentials
			errorValue.Delay = c.circuitCooldown
			if circuitOnFailure {
				c.blockFor(c.circuitCooldown)
			}
		} else if status.Code == "429" {
			errorValue.Class = ErrorRateLimit
			errorValue.Delay = retryAfter(response.Header.Get("Retry-After"), c.now())
			if circuitOnFailure {
				c.blockFor(errorValue.Delay)
			}
		} else if strings.HasPrefix(status.Code, "5") {
			errorValue.Class = ErrorServer
			retry = true
		}
		return nil, retry, errorValue
	}
	return body, false, nil
}

func (c *Client) blockFor(delay time.Duration) {
	c.circuitMu.Lock()
	until := c.now().Add(delay)
	if until.After(c.blockedUntil) {
		c.blockedUntil = until
	}
	c.circuitMu.Unlock()
}

func retryDelay() time.Duration {
	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return retryBaseDelay
	}
	jitter := time.Duration(binary.LittleEndian.Uint64(randomBytes[:]) % uint64(100*time.Millisecond))
	return retryBaseDelay + jitter
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		delay := time.Duration(seconds) * time.Second
		if delay > 15*time.Minute {
			return 15 * time.Minute
		}
		return delay
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		delay := retryAt.Sub(now)
		if delay > 15*time.Minute {
			return 15 * time.Minute
		}
		return delay
	}
	return time.Minute
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

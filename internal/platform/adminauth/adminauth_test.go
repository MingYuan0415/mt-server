package adminauth

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPasswordHashAndVerification(t *testing.T) {
	value, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidPasswordHash(value) || !VerifyPassword("correct horse battery staple", value) ||
		VerifyPassword("incorrect password", value) {
		t.Fatal("password verifier returned an invalid result")
	}
	if _, err := HashPassword("too-short"); err == nil {
		t.Fatal("expected short password rejection")
	}
}

func TestSessionsValidateCSRFExpireAndClear(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	sessions := NewSessions()
	sessions.now = func() time.Time { return now }
	token, csrf, err := sessions.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !sessions.ValidateCSRF(token, csrf) || sessions.ValidateCSRF(token, "wrong") {
		t.Fatal("unexpected CSRF validation")
	}
	now = now.Add(sessionIdle + time.Second)
	if _, ok := sessions.Validate(token); ok {
		t.Fatal("idle session did not expire")
	}
	token, _, _ = sessions.Create()
	sessions.Clear()
	if _, ok := sessions.Validate(token); ok {
		t.Fatal("session survived Clear")
	}
}

func TestTransportPolicyRequiresSecureOrExplicitInsecureMode(t *testing.T) {
	policy := NewTransportPolicy(false, true)
	request := httptest.NewRequest("POST", "http://api.example.com/admin/api/v1/session", nil)
	request.Header.Set("Origin", "https://api.example.com")
	if !policy.Secure(request) || !policy.AllowWrite(request) || !policy.SameOrigin(request) {
		t.Fatal("configured HTTPS proxy was rejected")
	}
	request.Header.Set("Origin", "https://API.EXAMPLE.COM")
	if !policy.SameOrigin(request) {
		t.Fatal("case-insensitive origin host was rejected")
	}
	request.Header.Set("Origin", "https://user@api.example.com")
	if policy.SameOrigin(request) {
		t.Fatal("origin with user information was accepted")
	}
	request.Header.Set("Origin", "https://api.example.com?unexpected=true")
	if policy.SameOrigin(request) {
		t.Fatal("origin with query data was accepted")
	}
	request.Header.Set("Origin", "https://api.example.com")
	directHTTPPolicy := NewTransportPolicy(false, false)
	request.Header.Set("X-Forwarded-Proto", "https")
	if directHTTPPolicy.AllowWrite(request) {
		t.Fatal("forwarded protocol header was trusted")
	}
	directTLS := httptest.NewRequest("POST", "https://api.example.com/admin/api/v1/session", nil)
	directTLS.TLS = &tls.ConnectionState{}
	directTLS.Header.Set("Origin", "https://api.example.com")
	if !policy.AllowWrite(directTLS) || !policy.SameOrigin(directTLS) {
		t.Fatal("direct TLS request was rejected")
	}
	insecure := NewTransportPolicy(true, false)
	directHTTP := httptest.NewRequest("POST", "http://api.example.com/admin/api/v1/session", nil)
	directHTTP.Header.Set("Origin", "http://api.example.com")
	if !insecure.AllowWrite(directHTTP) || !insecure.SameOrigin(directHTTP) {
		t.Fatal("explicit LAN HTTP mode was rejected")
	}
}

func TestLimiterBoundsAttempts(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	limiter := NewLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }
	if !limiter.Allow() || !limiter.Allow() || limiter.Allow() {
		t.Fatal("unexpected limiter result")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow() {
		t.Fatal("limiter did not reset after its window")
	}
}

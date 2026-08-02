package adminauth

import (
	"crypto/tls"
	"fmt"
	"net/http/httptest"
	"strings"
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
	unicodePassword := "天气服务管理密码天气服务"
	if _, err := HashPassword(unicodePassword); err != nil {
		t.Fatalf("valid Unicode password was rejected: %v", err)
	}
	if _, err := HashPassword("天气服务管理密码天气服"); err == nil {
		t.Fatal("expected 11-code-point password rejection")
	}
	if _, err := HashPassword(strings.Repeat("天", 43)); err == nil {
		t.Fatal("expected password longer than 128 UTF-8 bytes rejection")
	}
}

func TestNormalizePublicOrigins(t *testing.T) {
	values, err := NormalizePublicOrigins([]string{
		"https://API.Example.com:0443", "https://127.0.0.1:8443", "https://[2001:DB8::1]:9443",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://api.example.com", "https://127.0.0.1:8443", "https://[2001:db8::1]:9443",
	}
	if fmt.Sprint(values) != fmt.Sprint(want) {
		t.Fatalf("unexpected normalized origins %#v", values)
	}
	invalid := []string{
		"http://api.example.com", "https://user@api.example.com", "https://api.example.com/path",
		"https://api.example.com?x=1", "https://api.example.com#x", "https://bad_host.example.com",
		"https://-bad.example.com", "https://api.example.com:", "https://api.example.com:65536",
	}
	for _, value := range invalid {
		if _, err := NormalizePublicOrigin(value); err == nil {
			t.Fatalf("invalid origin %q was accepted", value)
		}
	}
	if _, err := NormalizePublicOrigins([]string{
		"https://api.example.com", "https://API.EXAMPLE.COM:443",
	}); err == nil {
		t.Fatal("duplicate canonical origin was accepted")
	}
	overLimit := make([]string, MaximumPublicOrigins+1)
	for index := range overLimit {
		overLimit[index] = fmt.Sprintf("https://host-%d.example.com", index)
	}
	if _, err := NormalizePublicOrigins(overLimit); err == nil {
		t.Fatal("origin limit was not enforced")
	}
}

func TestTransportPolicyHotSwitchesProxyOrigins(t *testing.T) {
	policy := NewTransportPolicy(false, true)
	policy.ReplacePublicOrigins([]string{"https://old.example.com"})
	request := httptest.NewRequest("POST", "http://mt-server:8080/admin/api/v1/session", nil)
	request.Header.Set("Origin", "https://old.example.com")
	if !policy.SameOrigin(request) {
		t.Fatal("initial origin was rejected")
	}
	policy.ReplacePublicOrigins([]string{"https://new.example.com"})
	if policy.SameOrigin(request) {
		t.Fatal("removed origin survived hot switch")
	}
	request.Header.Set("Origin", "https://new.example.com")
	if !policy.SameOrigin(request) {
		t.Fatal("new origin was not activated")
	}
	if _, err := policy.ValidatePublicOrigins(nil); err == nil {
		t.Fatal("proxy policy accepted an empty origin list")
	}
}

func TestTransportPolicyAcceptsExplicitPublicOrigins(t *testing.T) {
	policy := NewTransportPolicy(false, true)
	policy.ReplacePublicOrigins([]string{
		"https://api.example.com", "https://[2001:db8::1]:8443",
	})
	for _, origin := range []string{
		"https://API.EXAMPLE.COM:443",
		"https://[2001:DB8::1]:8443",
	} {
		request := httptest.NewRequest("POST", "http://mt-server:8080/admin/api/v1/session", nil)
		request.Host = "mt-server:8080"
		request.Header.Set("Origin", origin)
		request.Header.Set("Forwarded", "host=forged.example.com;proto=http")
		request.Header.Set("X-Forwarded-Host", "forged.example.com")
		request.Header.Set("X-Forwarded-Proto", "http")
		if !policy.SameOrigin(request) || !policy.Secure(request) {
			t.Fatalf("allowed public origin %q was rejected", origin)
		}
	}

	rejected := httptest.NewRequest("POST", "http://mt-server:8080/admin/api/v1/session", nil)
	rejected.Header.Set("Origin", "https://other.example.com")
	if policy.SameOrigin(rejected) {
		t.Fatal("non-allowlisted origin was accepted")
	}

	duplicate := httptest.NewRequest("POST", "http://mt-server:8080/admin/api/v1/session", nil)
	duplicate.Header.Add("Origin", "https://api.example.com")
	duplicate.Header.Add("Origin", "https://api.example.com")
	if policy.SameOrigin(duplicate) {
		t.Fatal("multiple Origin headers were accepted")
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
	policy.ReplacePublicOrigins([]string{"https://api.example.com"})
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
	request.Header.Set("Origin", "https://api.example.com:")
	if policy.SameOrigin(request) {
		t.Fatal("origin with an empty explicit port was accepted")
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

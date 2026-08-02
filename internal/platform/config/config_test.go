package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	value, err := load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if value.ListenAddr != ":8080" || value.LogLevel != "info" ||
		value.StateDir != "/var/lib/mt-server" || value.AdminAllowInsecureHTTP ||
		value.AdminBehindHTTPSProxy {
		t.Fatalf("unexpected defaults %#v", value)
	}
}

func TestLoadPublicOrigins(t *testing.T) {
	values := map[string]string{
		"MT_ADMIN_BEHIND_HTTPS_PROXY": "true",
		"MT_ADMIN_PUBLIC_ORIGINS":     "https://API.Example.com:443, https://[2001:db8::1]:8443",
	}
	value, err := load(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://api.example.com", "https://[2001:db8::1]:8443"}
	if fmt.Sprint(value.AdminPublicOrigins) != fmt.Sprint(want) {
		t.Fatalf("unexpected origins %#v", value.AdminPublicOrigins)
	}
}

func TestLoadRejectsInvalidPublicOrigins(t *testing.T) {
	invalid := []string{
		"http://api.example.com",
		"https://user@api.example.com",
		"https://api.example.com/path",
		"https://api.example.com?query=1",
		"https://api.example.com#fragment",
		"https://bad_host.example.com",
		"https://-bad.example.com",
		"https://api.example.com:",
		"https://[2001:db8::1]:",
		"https://api.example.com:65536",
		"https://api.example.com,https://API.EXAMPLE.COM:443",
		strings.Repeat("https://a.example.com,", maximumPublicOrigins) + "https://b.example.com",
	}
	for _, origins := range invalid {
		t.Run(origins, func(t *testing.T) {
			values := map[string]string{
				"MT_ADMIN_BEHIND_HTTPS_PROXY": "true",
				"MT_ADMIN_PUBLIC_ORIGINS":     origins,
			}
			if _, err := load(func(name string) string { return values[name] }); err == nil {
				t.Fatal("expected public-origin validation failure")
			}
		})
	}
}

func TestLoadPublicOriginsRequireStrictProxyMode(t *testing.T) {
	for _, values := range []map[string]string{
		{"MT_ADMIN_PUBLIC_ORIGINS": "https://api.example.com"},
		{
			"MT_ADMIN_PUBLIC_ORIGINS":      "https://api.example.com",
			"MT_ADMIN_BEHIND_HTTPS_PROXY":  "true",
			"MT_ADMIN_ALLOW_INSECURE_HTTP": "true",
		},
	} {
		if _, err := load(func(name string) string { return values[name] }); err == nil {
			t.Fatal("expected strict proxy-mode validation failure")
		}
	}
}

func TestLoadInfrastructureOverrides(t *testing.T) {
	values := map[string]string{
		"MT_LISTEN_ADDR":               ":9090",
		"MT_LOG_LEVEL":                 "debug",
		"MT_STATE_DIR":                 "/data",
		"MT_ADMIN_ALLOW_INSECURE_HTTP": "true",
		"MT_ADMIN_BEHIND_HTTPS_PROXY":  "true",
	}
	value, err := load(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if value.ListenAddr != ":9090" || !value.AdminAllowInsecureHTTP ||
		!value.AdminBehindHTTPSProxy {
		t.Fatalf("unexpected configuration %#v", value)
	}
}

func TestLoadRejectsInvalidInfrastructure(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "listen address", key: "MT_LISTEN_ADDR", value: "bad"},
		{name: "log level", key: "MT_LOG_LEVEL", value: "trace"},
		{name: "relative state", key: "MT_STATE_DIR", value: "state"},
		{name: "boolean", key: "MT_ADMIN_ALLOW_INSECURE_HTTP", value: "sometimes"},
		{name: "HTTPS proxy boolean", key: "MT_ADMIN_BEHIND_HTTPS_PROXY", value: "sometimes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{test.key: test.value}
			if _, err := load(func(name string) string { return values[name] }); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

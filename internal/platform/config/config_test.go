package config

import (
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

func TestLoadRejectsMixedManagementTransportModes(t *testing.T) {
	values := map[string]string{
		"MT_ADMIN_BEHIND_HTTPS_PROXY":  "true",
		"MT_ADMIN_ALLOW_INSECURE_HTTP": "true",
	}
	if _, err := load(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected mutually exclusive management mode rejection")
	}
}

func TestLoadInfrastructureOverrides(t *testing.T) {
	values := map[string]string{
		"MT_LISTEN_ADDR":               ":9090",
		"MT_LOG_LEVEL":                 "debug",
		"MT_STATE_DIR":                 "/data",
		"MT_ADMIN_ALLOW_INSECURE_HTTP": "false",
		"MT_ADMIN_BEHIND_HTTPS_PROXY":  "true",
	}
	value, err := load(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if value.ListenAddr != ":9090" || value.AdminAllowInsecureHTTP ||
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
		{name: "client IP nets", key: "MT_TRUSTED_CLIENT_IP_NETS", value: "10.0.0.1"},
		{name: "client IP nets mixed", key: "MT_TRUSTED_CLIENT_IP_NETS", value: "10.0.0.0/8,bad"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{test.key: test.value}
			if _, err := load(func(name string) string { return values[name] }); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestLoadGeoIPConfiguration(t *testing.T) {
	values := map[string]string{
		"MT_GEOIP_DB":                 "/var/lib/geoip/GeoLite2-City.mmdb",
		"MT_TRUSTED_CLIENT_IP_HEADER": "CF-Connecting-IP",
		"MT_TRUSTED_CLIENT_IP_NETS":   "172.30.0.0/16, ::1/128",
	}
	value, err := load(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if value.GeoIPDBPath != "/var/lib/geoip/GeoLite2-City.mmdb" ||
		value.TrustedClientIPHeader != "CF-Connecting-IP" ||
		len(value.TrustedClientIPNets) != 2 {
		t.Fatalf("unexpected geoip configuration %#v", value)
	}
}

func TestLoadRejectsHeaderWithoutTrustedNets(t *testing.T) {
	values := map[string]string{"MT_TRUSTED_CLIENT_IP_HEADER": "CF-Connecting-IP"}
	if _, err := load(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected header-without-nets rejection")
	}
}

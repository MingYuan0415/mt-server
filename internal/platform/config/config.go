// Package config loads process-level infrastructure configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config contains settings that must be known before persistent application
// state can be opened.
type Config struct {
	ListenAddr             string
	LogLevel               string
	StateDir               string
	AdminAllowInsecureHTTP bool
	AdminBehindHTTPSProxy  bool
	GeoIPDBPath            string
	TrustedClientIPHeader  string
	TrustedClientIPNets    []netip.Prefix
}

// Load reads and validates process-level configuration.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	listenAddr := valueOrDefault(getenv("MT_LISTEN_ADDR"), ":8080")
	if _, _, err := net.SplitHostPort(listenAddr); err != nil {
		return Config{}, fmt.Errorf("MT_LISTEN_ADDR: %w", err)
	}

	logLevel := valueOrDefault(getenv("MT_LOG_LEVEL"), "info")
	switch logLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("MT_LOG_LEVEL: unsupported value %q", logLevel)
	}

	stateDir := valueOrDefault(getenv("MT_STATE_DIR"), "/var/lib/mt-server")
	if !filepath.IsAbs(stateDir) {
		return Config{}, errors.New("MT_STATE_DIR: path must be absolute")
	}

	allowInsecure, err := boolValue(getenv("MT_ADMIN_ALLOW_INSECURE_HTTP"), false)
	if err != nil {
		return Config{}, fmt.Errorf("MT_ADMIN_ALLOW_INSECURE_HTTP: %w", err)
	}
	behindHTTPSProxy, err := boolValue(getenv("MT_ADMIN_BEHIND_HTTPS_PROXY"), false)
	if err != nil {
		return Config{}, fmt.Errorf("MT_ADMIN_BEHIND_HTTPS_PROXY: %w", err)
	}
	if behindHTTPSProxy && allowInsecure {
		return Config{}, errors.New("MT_ADMIN_BEHIND_HTTPS_PROXY and MT_ADMIN_ALLOW_INSECURE_HTTP are mutually exclusive")
	}

	geoIPDBPath := strings.TrimSpace(getenv("MT_GEOIP_DB"))
	trustedHeader := strings.TrimSpace(getenv("MT_TRUSTED_CLIENT_IP_HEADER"))
	trustedNets, err := parsePrefixes(getenv("MT_TRUSTED_CLIENT_IP_NETS"))
	if err != nil {
		return Config{}, fmt.Errorf("MT_TRUSTED_CLIENT_IP_NETS: %w", err)
	}
	if trustedHeader != "" && len(trustedNets) == 0 {
		return Config{}, errors.New("MT_TRUSTED_CLIENT_IP_HEADER requires MT_TRUSTED_CLIENT_IP_NETS")
	}

	return Config{
		ListenAddr:             listenAddr,
		LogLevel:               logLevel,
		StateDir:               stateDir,
		AdminAllowInsecureHTTP: allowInsecure,
		AdminBehindHTTPSProxy:  behindHTTPSProxy,
		GeoIPDBPath:            geoIPDBPath,
		TrustedClientIPHeader:  trustedHeader,
		TrustedClientIPNets:    trustedNets,
	}, nil
}

func parsePrefixes(value string) ([]netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, errors.New("invalid CIDR " + part)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func boolValue(value string, fallback bool) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New("value must be true or false")
	}
	return parsed, nil
}

func valueOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

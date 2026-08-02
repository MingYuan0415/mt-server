// Package config loads process-level infrastructure configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maximumPublicOrigins = 16

// Config contains settings that must be known before persistent application
// state can be opened.
type Config struct {
	ListenAddr             string
	LogLevel               string
	StateDir               string
	AdminAllowInsecureHTTP bool
	AdminBehindHTTPSProxy  bool
	AdminPublicOrigins     []string
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
	publicOrigins, err := parsePublicOrigins(getenv("MT_ADMIN_PUBLIC_ORIGINS"))
	if err != nil {
		return Config{}, fmt.Errorf("MT_ADMIN_PUBLIC_ORIGINS: %w", err)
	}
	if len(publicOrigins) > 0 && (!behindHTTPSProxy || allowInsecure) {
		return Config{}, errors.New("MT_ADMIN_PUBLIC_ORIGINS: requires HTTPS proxy mode and disallows insecure HTTP")
	}

	return Config{
		ListenAddr:             listenAddr,
		LogLevel:               logLevel,
		StateDir:               stateDir,
		AdminAllowInsecureHTTP: allowInsecure,
		AdminBehindHTTPSProxy:  behindHTTPSProxy,
		AdminPublicOrigins:     publicOrigins,
	}, nil
}

func parsePublicOrigins(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > maximumPublicOrigins {
		return nil, fmt.Errorf("at most %d origins are allowed", maximumPublicOrigins)
	}
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin, err := canonicalPublicOrigin(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		if _, exists := seen[origin]; exists {
			return nil, errors.New("duplicate origin")
		}
		seen[origin] = struct{}{}
		result = append(result, origin)
	}
	return result, nil
}

func canonicalPublicOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("invalid HTTPS origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if !validPublicHostname(hostname) {
		return "", errors.New("invalid HTTPS origin")
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("invalid HTTPS origin")
		}
	}
	if port == "443" {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return "https://" + host, nil
}

func validPublicHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 || strings.ContainsAny(hostname, " \t\r\n") {
		return false
	}
	if net.ParseIP(hostname) != nil {
		return true
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
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

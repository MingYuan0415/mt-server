package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/admin"
	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

type runtime struct {
	mu    sync.Mutex
	ready bool
}

type preparedChange struct{ activate func() }

func (p *preparedChange) Activate() {
	if p.activate != nil {
		p.activate()
		p.activate = nil
	}
}

func (p *preparedChange) Discard() { p.activate = nil }

func (r *runtime) Test(_ context.Context, _ state.State,
	point *location.Point) (weather.Verification, []string, string, error) {
	if point == nil {
		return weather.Verification{}, nil, "", location.ErrRequired
	}
	now := time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC)
	return weather.Verification{
		Source: weather.Source{
			ID: "qweather", Name: "QWeather", AttributionURL: "https://www.qweather.com/",
		},
		Location: weather.PublicLocation{
			City: "Example", Source: "browser", Provider: "browser", Precision: "coarse",
			LocationKey: "9f4a2b3c8d1e5f06",
		},
		TestedAt:  now,
		UpdatedAt: now.Add(-time.Minute),
		Data: weather.Current{
			ObservedAt: now.Add(-2 * time.Minute), TemperatureC: 28, FeelsLikeC: 31,
			ConditionCode: "101", ConditionText: "多云", HumidityPercent: 72,
			WindSpeedKMH: 8,
		},
	}, []string{"current", "alerts"}, "test-public-key-fingerprint", nil
}

func (r *runtime) Prepare(state.State) (platform.PreparedChange, error) {
	return &preparedChange{activate: func() {
		r.mu.Lock()
		r.ready = true
		r.mu.Unlock()
	}}, nil
}

func (*runtime) PrepareTokens([]state.DeviceToken) (platform.PreparedChange, error) {
	return &preparedChange{}, nil
}

func (r *runtime) Ready() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready {
		return errors.New("setup required")
	}
	return nil
}

func (r *runtime) Diagnostics() (weather.Diagnostics, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready {
		return weather.Diagnostics{}, errors.New("setup required")
	}
	now := time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC)
	return weather.Diagnostics{
		GeneratedAt: now, RuntimeStarted: now.Add(-time.Hour),
		Provider: weather.ProviderDiagnostics{Status: "ready"},
		Kinds: map[weather.Kind]weather.KindDiagnostics{
			weather.KindCurrent: {}, weather.KindHourly: {},
			weather.KindDaily: {}, weather.KindAlerts: {},
		},
	}, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	store, err := state.NewStore(os.Getenv("MT_STATE_DIR"))
	if err != nil {
		logger.Error("open state", "error", err)
		os.Exit(1)
	}
	management, err := admin.New(store, &runtime{}, adminauth.NewSessions(),
		adminauth.NewTransportPolicy(false, true), logger, "web-test")
	if err != nil {
		logger.Error("create management handler", "error", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	management.RegisterRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, request *http.Request) {
		httpapi.WriteError(w, request, http.StatusNotFound, "not_found", "resource not found")
	})
	backend := &http.Server{
		Addr:              "127.0.0.1:18080",
		Handler:           httpapi.RequestContext(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	target, _ := url.Parse("http://127.0.0.1:18080")
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = "mt-server:8080"
	}
	frontend := &http.Server{
		Addr:              "127.0.0.1:18443",
		Handler:           proxy,
		ReadHeaderTimeout: 5 * time.Second,
	}
	backendListener, err := net.Listen("tcp", backend.Addr)
	if err != nil {
		logger.Error("listen backend", "error", err)
		os.Exit(1)
	}
	certificate, err := testCertificate(os.Getenv("MT_STATE_DIR"))
	if err != nil {
		logger.Error("create test certificate", "error", err)
		os.Exit(1)
	}
	frontendListener, err := tls.Listen("tcp", frontend.Addr, &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		logger.Error("listen frontend", "error", err)
		os.Exit(1)
	}
	go func() {
		if err := frontend.Serve(frontendListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve frontend", "error", err)
			os.Exit(1)
		}
	}()
	logger.Info("server listening")
	if err := backend.Serve(backendListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
}

func testCertificate(directory string) (tls.Certificate, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "admin.example.test"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"admin.example.test", "new.example.test"},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template,
		&privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificatePath := filepath.Join(directory, "test-cert.pem")
	privatePath := filepath.Join(directory, "test-key.pem")
	if err := os.WriteFile(certificatePath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(privatePath,
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certificatePath, privatePath)
}

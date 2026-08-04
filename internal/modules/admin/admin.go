// Package admin provides the local management API and embedded web interface.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/weather"
	"github.com/MingYuan0415/mt-server/internal/platform"
	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
	"github.com/MingYuan0415/mt-server/internal/platform/location"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
	"github.com/MingYuan0415/mt-server/internal/providers/qweather"
)

const (
	sessionCookieName  = "mt_admin_session"
	maximumJSONBody    = 64 * 1024
	stateWarningHeader = "X-MT-State-Warning"
	stateWarningValue  = "durability_unconfirmed"
)

var (
	errQWeatherTestBusy        = errors.New("QWeather connection test is already running")
	errQWeatherTestRateLimited = errors.New("QWeather connection test rate limit exceeded")
)

// Runtime applies validated state to the live device API.
type Runtime interface {
	Test(context.Context, state.State, *location.Point) (weather.Verification, string, error)
	Prepare(state.State) (platform.PreparedChange, error)
	PrepareTokens([]state.DeviceToken) (platform.PreparedChange, error)
	Ready() error
	Diagnostics() (weather.Diagnostics, error)
}

// Handler owns management authentication and configuration workflows.
type Handler struct {
	store         *state.Store
	runtime       Runtime
	sessions      *adminauth.Sessions
	transport     *adminauth.TransportPolicy
	setupLimit    *adminauth.Limiter
	loginLimit    *adminauth.Limiter
	testLimit     *adminauth.Limiter
	logger        *slog.Logger
	version       string
	passwordSlots chan struct{}
	testSlot      chan struct{}
	publicCSRF    string
	changeMu      sync.Mutex
}

// New constructs the management interface.
func New(store *state.Store, runtime Runtime, sessions *adminauth.Sessions,
	transport *adminauth.TransportPolicy, logger *slog.Logger, version string) (*Handler, error) {
	publicCSRF, err := randomValue()
	if err != nil {
		return nil, fmt.Errorf("generate management CSRF token: %w", err)
	}
	return &Handler{
		store: store, runtime: runtime, sessions: sessions, transport: transport,
		setupLimit: adminauth.NewLimiter(5, time.Minute),
		loginLimit: adminauth.NewLimiter(5, time.Minute),
		testLimit:  adminauth.NewLimiter(6, time.Minute),
		logger:     logger, version: version, passwordSlots: make(chan struct{}, 2),
		testSlot:   make(chan struct{}, 1),
		publicCSRF: publicCSRF,
	}, nil
}

// RegisterRoutes installs web assets and management APIs.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	h.registerAssets(mux)
	mux.HandleFunc("GET /admin/api/v1/status", h.status)
	mux.HandleFunc("POST /admin/api/v1/setup", h.setup)
	mux.HandleFunc("POST /admin/api/v1/session", h.login)
	mux.HandleFunc("GET /admin/api/v1/session", h.requireSession(h.session, false))
	mux.HandleFunc("DELETE /admin/api/v1/session", h.requireSession(h.logout, true))
	mux.HandleFunc("GET /admin/api/v1/settings/qweather", h.requireSession(h.getQWeather, false))
	mux.HandleFunc("POST /admin/api/v1/settings/qweather/test", h.requireSession(h.testQWeather, true))
	mux.HandleFunc("PUT /admin/api/v1/settings/qweather", h.requireSession(h.putQWeather, true))
	mux.HandleFunc("GET /admin/api/v1/settings/admin-origins", h.requireSession(h.listAdminOrigins, false))
	mux.HandleFunc("POST /admin/api/v1/settings/admin-origins", h.requireSession(h.addAdminOrigin, true))
	mux.HandleFunc("DELETE /admin/api/v1/settings/admin-origins/{id}", h.requireSession(h.deleteAdminOrigin, true))
	mux.HandleFunc("GET /admin/api/v1/device-tokens", h.requireSession(h.listTokens, false))
	mux.HandleFunc("GET /admin/api/v1/diagnostics", h.requireSession(h.diagnostics, false))
	mux.HandleFunc("POST /admin/api/v1/device-tokens", h.requireSession(h.createToken, true))
	mux.HandleFunc("DELETE /admin/api/v1/device-tokens/{id}", h.requireSession(h.deleteToken, true))
	mux.HandleFunc("PUT /admin/api/v1/account/password", h.requireSession(h.changePassword, true))
}

func (h *Handler) diagnostics(w http.ResponseWriter, r *http.Request) {
	value, err := h.runtime.Diagnostics()
	if err != nil {
		httpapi.WriteError(w, r, http.StatusServiceUnavailable,
			"diagnostics_unavailable", "runtime diagnostics are unavailable")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, value)
}

type setupRequest struct {
	Password     string        `json:"password"`
	QWeather     qweatherInput `json:"qweather"`
	DeviceName   string        `json:"device_name"`
	AdminOrigins []string      `json:"admin_origins,omitempty"`
}

type qweatherInput struct {
	APIHost       string             `json:"api_host"`
	ProjectID     string             `json:"project_id"`
	CredentialID  string             `json:"credential_id"`
	PrivateKeyPEM string             `json:"private_key_pem,omitempty"`
	TestLocation  *testLocationInput `json:"test_location,omitempty"`
}

type testLocationInput struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	City      string   `json:"city,omitempty"`
	Region    string   `json:"region,omitempty"`
	Country   string   `json:"country,omitempty"`
	Timezone  string   `json:"timezone,omitempty"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type tokenRequest struct {
	Name string `json:"name"`
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type originRequest struct {
	Origin string `json:"origin"`
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	_, err := h.store.Load()
	configured := err == nil
	if err != nil && !errors.Is(err, state.ErrNotInitialized) {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_unavailable", "application state is unavailable")
		return
	}
	ready := "setup_required"
	durability := "not_applicable"
	if configured {
		durability = "confirmed"
		if !h.store.DurabilityConfirmed() {
			durability = "unconfirmed"
		}
		if err := h.runtime.Ready(); err == nil {
			ready = "ready"
		} else {
			ready = "unavailable"
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"configured":        configured,
		"status":            ready,
		"version":           h.version,
		"secure_transport":  h.transport.Secure(r),
		"admin_origin_mode": h.transport.OriginMode(),
		"state_durability":  durability,
		"csrf_token":        h.publicCSRF,
	})
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	if !h.preparePublicWriteWithoutOrigin(w, r, h.setupLimit) {
		return
	}
	if _, err := h.store.Load(); err == nil {
		if !h.requireRequestOrigin(w, r) {
			return
		}
		httpapi.WriteError(w, r, http.StatusConflict,
			"already_configured", "service setup is already complete")
		return
	} else if !errors.Is(err, state.ErrNotInitialized) {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_unavailable", "application state is unavailable")
		return
	}
	var request setupRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	origins, err := h.transport.ValidatePublicOrigins(request.AdminOrigins)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest,
			"invalid_admin_origin", err.Error())
		return
	}
	if !h.transport.SetupOriginAllowed(r, origins) {
		h.logWriteRejection(r, "origin")
		httpapi.WriteError(w, r, http.StatusForbidden,
			"origin_rejected", "management request origin is not present in setup settings")
		return
	}
	if !h.acquirePasswordSlot(w, r) {
		return
	}
	passwordHash, err := adminauth.HashPassword(request.Password)
	h.releasePasswordSlot()
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	rawToken, tokenState, err := newDeviceToken(request.DeviceName, time.Now())
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest, "invalid_device_name", err.Error())
		return
	}
	value := state.State{
		SchemaVersion: state.SchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Admin:         state.AdminState{Password: passwordHash, PublicOrigins: origins},
		QWeather:      qweatherState(request.QWeather, ""),
		Cache:         state.DefaultCache(),
		DeviceTokens:  []state.DeviceToken{tokenState},
	}
	verification, fingerprint, err := h.testCandidate(r, value, request.QWeather.TestLocation)
	if err != nil {
		h.writeTestError(w, r, err, false)
		return
	}
	prepared, err := h.runtime.Prepare(value)
	if err != nil {
		h.logger.Error("prepare initialized runtime failed", "error", err)
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"runtime_prepare_failed", "configuration could not be prepared")
		return
	}
	defer prepared.Discard()
	sessionToken, csrf, err := h.sessions.Create()
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"session_failed", "could not create administrator session")
		return
	}
	keepSession := false
	defer func() {
		if !keepSession {
			h.sessions.Delete(sessionToken)
		}
	}()
	writeResult, err := h.store.CommitInitial(value)
	if err != nil {
		if errors.Is(err, state.ErrAlreadyInitialized) {
			httpapi.WriteError(w, r, http.StatusConflict,
				"already_configured", "service setup is already complete")
			return
		}
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_write_failed", "could not save application state")
		return
	}
	h.reportWriteResult(w, r, writeResult)
	h.transport.ReplacePublicOrigins(origins)
	prepared.Activate()
	h.setupLimit.Reset()
	keepSession = true
	h.setSessionCookie(w, r, sessionToken)
	h.logger.Info("service setup completed")
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"csrf_token":                      csrf,
		"device_token":                    rawToken,
		"device":                          publicToken(tokenState),
		"qweather_public_key_fingerprint": fingerprint,
		"verification":                    verification,
		"tested_capabilities":             []string{"current", "alerts"},
	})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.preparePublicWrite(w, r, h.loginLimit) {
		return
	}
	var request loginRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := h.store.Load()
	if errors.Is(err, state.ErrNotInitialized) {
		httpapi.WriteError(w, r, http.StatusConflict, "setup_required", "service setup is required")
		return
	}
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_unavailable", "application state is unavailable")
		return
	}
	if !h.acquirePasswordSlot(w, r) {
		return
	}
	valid := adminauth.VerifyPassword(request.Password, value.Admin.Password)
	h.releasePasswordSlot()
	if !valid {
		httpapi.WriteError(w, r, http.StatusUnauthorized,
			"invalid_credentials", "administrator password is incorrect")
		return
	}
	h.loginLimit.Reset()
	sessionToken, csrf, err := h.sessions.Create()
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"session_failed", "could not create administrator session")
		return
	}
	h.setSessionCookie(w, r, sessionToken)
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"csrf_token": csrf})
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	_, csrf, ok := h.sessionFromRequest(r)
	if !ok {
		httpapi.WriteError(w, r, http.StatusUnauthorized, "admin_unauthorized", "administrator login required")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"csrf_token": csrf})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	token, _, _ := h.sessionFromRequest(r)
	h.sessions.Delete(token)
	h.clearSessionCookie(w, r)
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (h *Handler) getQWeather(w http.ResponseWriter, r *http.Request) {
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	fingerprint, _ := qweather.PublicKeyFingerprint([]byte(value.QWeather.PrivateKeyPEM))
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"api_host":               value.QWeather.APIHost,
		"project_id":             value.QWeather.ProjectID,
		"credential_id":          value.QWeather.CredentialID,
		"private_key_configured": value.QWeather.PrivateKeyPEM != "",
		"public_key_fingerprint": fingerprint,
		"language":               value.QWeather.Language,
		"unit":                   value.QWeather.Unit,
	})
}

func (h *Handler) testQWeather(w http.ResponseWriter, r *http.Request) {
	var request qweatherInput
	if !decodeJSON(w, r, &request) {
		return
	}
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	value.QWeather = qweatherState(request, value.QWeather.PrivateKeyPEM)
	verification, fingerprint, err := h.testCandidate(r, value, request.TestLocation)
	if err != nil {
		h.writeTestError(w, r, err, false)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "public_key_fingerprint": fingerprint, "verification": verification,
		"tested_capabilities": []string{"current", "alerts"},
	})
}

func (h *Handler) putQWeather(w http.ResponseWriter, r *http.Request) {
	var request qweatherInput
	if !decodeJSON(w, r, &request) {
		return
	}
	h.changeMu.Lock()
	defer h.changeMu.Unlock()
	previous, ok := h.loadState(w, r)
	if !ok {
		return
	}
	candidate := previous
	candidate.QWeather = qweatherState(request, previous.QWeather.PrivateKeyPEM)
	candidate.UpdatedAt = time.Now().UTC()
	verification, fingerprint, err := h.testCandidate(r, candidate, request.TestLocation)
	if err != nil {
		h.writeTestError(w, r, err, true)
		return
	}
	prepared, err := h.runtime.Prepare(candidate)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"runtime_prepare_failed", "new settings could not be prepared")
		return
	}
	defer prepared.Discard()
	if !h.saveAndActivate(w, r, candidate, prepared) {
		return
	}
	h.logger.Info("QWeather settings updated")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "saved", "public_key_fingerprint": fingerprint, "verification": verification,
		"tested_capabilities": []string{"current", "alerts"},
	})
}

func (h *Handler) listAdminOrigins(w http.ResponseWriter, r *http.Request) {
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"mode": h.transport.OriginMode(), "maximum": adminauth.MaximumPublicOrigins,
		"origins": publicOrigins(value.Admin.PublicOrigins),
	})
}

func (h *Handler) addAdminOrigin(w http.ResponseWriter, r *http.Request) {
	var request originRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	origin, err := adminauth.NormalizePublicOrigin(request.Origin)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest,
			"invalid_admin_origin", err.Error())
		return
	}
	h.changeMu.Lock()
	defer h.changeMu.Unlock()
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	for _, existing := range value.Admin.PublicOrigins {
		if existing == origin {
			httpapi.WriteError(w, r, http.StatusConflict,
				"admin_origin_exists", "management origin already exists")
			return
		}
	}
	if len(value.Admin.PublicOrigins) >= adminauth.MaximumPublicOrigins {
		httpapi.WriteError(w, r, http.StatusConflict,
			"admin_origin_limit_reached", "management origin limit reached")
		return
	}
	value.Admin.PublicOrigins = append(value.Admin.PublicOrigins, origin)
	value.UpdatedAt = time.Now().UTC()
	normalized, err := h.transport.ValidatePublicOrigins(value.Admin.PublicOrigins)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest,
			"invalid_admin_origin", err.Error())
		return
	}
	writeResult, err := h.store.Save(value)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_write_failed", "could not save management origin")
		return
	}
	h.reportWriteResult(w, r, writeResult)
	h.transport.ReplacePublicOrigins(normalized)
	h.logger.Info("management origin added")
	httpapi.WriteJSON(w, http.StatusCreated, publicOrigin(origin))
}

func (h *Handler) deleteAdminOrigin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.changeMu.Lock()
	defer h.changeMu.Unlock()
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	target := ""
	filtered := make([]string, 0, len(value.Admin.PublicOrigins))
	for _, origin := range value.Admin.PublicOrigins {
		if adminauth.PublicOriginID(origin) == id {
			target = origin
			continue
		}
		filtered = append(filtered, origin)
	}
	if target == "" {
		httpapi.WriteError(w, r, http.StatusNotFound,
			"admin_origin_not_found", "management origin not found")
		return
	}
	if h.transport.BehindHTTPSProxy() && len(filtered) == 0 {
		httpapi.WriteError(w, r, http.StatusConflict,
			"last_admin_origin", "the last management origin cannot be removed in proxy mode")
		return
	}
	requestOrigin, validOrigin := adminauth.RequestOrigin(r)
	if validOrigin && requestOrigin == target {
		httpapi.WriteError(w, r, http.StatusConflict,
			"current_admin_origin", "the current management origin cannot be removed")
		return
	}
	normalized, err := h.transport.ValidatePublicOrigins(filtered)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest,
			"invalid_admin_origin", err.Error())
		return
	}
	value.Admin.PublicOrigins = normalized
	value.UpdatedAt = time.Now().UTC()
	writeResult, err := h.store.Save(value)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_write_failed", "could not remove management origin")
		return
	}
	h.reportWriteResult(w, r, writeResult)
	h.transport.ReplacePublicOrigins(normalized)
	h.sessions.Clear()
	h.clearSessionCookie(w, r)
	h.logger.Info("management origin removed")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	tokens := make([]map[string]any, 0, len(value.DeviceTokens))
	for _, token := range value.DeviceTokens {
		tokens = append(tokens, publicToken(token))
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var request tokenRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	h.changeMu.Lock()
	defer h.changeMu.Unlock()
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	if len(value.DeviceTokens) >= 32 {
		httpapi.WriteError(w, r, http.StatusConflict,
			"token_limit_reached", "at most 32 device tokens are allowed")
		return
	}
	raw, token, err := newDeviceToken(request.Name, time.Now())
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest, "invalid_device_name", err.Error())
		return
	}
	value.DeviceTokens = append(value.DeviceTokens, token)
	value.UpdatedAt = time.Now().UTC()
	prepared, err := h.runtime.PrepareTokens(value.DeviceTokens)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"runtime_prepare_failed", "device token could not be prepared")
		return
	}
	defer prepared.Discard()
	writeResult, err := h.store.Save(value)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_write_failed", "could not save device token")
		return
	}
	h.reportWriteResult(w, r, writeResult)
	prepared.Activate()
	h.logger.Info("device token created")
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"device_token": raw, "device": publicToken(token),
	})
}

func (h *Handler) deleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.changeMu.Lock()
	defer h.changeMu.Unlock()
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	if len(value.DeviceTokens) == 1 {
		httpapi.WriteError(w, r, http.StatusConflict,
			"last_token", "create a replacement token before revoking the last token")
		return
	}
	filtered := make([]state.DeviceToken, 0, len(value.DeviceTokens)-1)
	found := false
	for _, token := range value.DeviceTokens {
		if token.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, token)
	}
	if !found {
		httpapi.WriteError(w, r, http.StatusNotFound, "device_not_found", "device token not found")
		return
	}
	value.DeviceTokens = filtered
	value.UpdatedAt = time.Now().UTC()
	prepared, err := h.runtime.PrepareTokens(value.DeviceTokens)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"runtime_prepare_failed", "device token revocation could not be prepared")
		return
	}
	defer prepared.Discard()
	writeResult, err := h.store.Save(value)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_write_failed", "could not revoke device token")
		return
	}
	h.reportWriteResult(w, r, writeResult)
	prepared.Activate()
	h.logger.Info("device token revoked")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var request passwordRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	h.changeMu.Lock()
	defer h.changeMu.Unlock()
	value, ok := h.loadState(w, r)
	if !ok {
		return
	}
	if !h.acquirePasswordSlot(w, r) {
		return
	}
	valid := adminauth.VerifyPassword(request.CurrentPassword, value.Admin.Password)
	if !valid {
		h.releasePasswordSlot()
		httpapi.WriteError(w, r, http.StatusUnauthorized,
			"invalid_credentials", "current administrator password is incorrect")
		return
	}
	passwordHash, err := adminauth.HashPassword(request.NewPassword)
	h.releasePasswordSlot()
	if err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	value.Admin.Password = passwordHash
	value.UpdatedAt = time.Now().UTC()
	writeResult, err := h.store.Save(value)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_write_failed", "could not update administrator password")
		return
	}
	h.reportWriteResult(w, r, writeResult)
	h.sessions.Clear()
	h.clearSessionCookie(w, r)
	h.logger.Info("administrator password updated")
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "password_updated"})
}

func (h *Handler) requireSession(next http.HandlerFunc, write bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, _, ok := h.sessionFromRequest(r)
		if !ok {
			httpapi.WriteError(w, r, http.StatusUnauthorized,
				"admin_unauthorized", "administrator login required")
			return
		}
		if write {
			if !h.transport.AllowWrite(r) {
				httpapi.WriteError(w, r, http.StatusForbidden,
					"https_required", "HTTPS is required for management changes")
				return
			}
			if !h.transport.SameOrigin(r) {
				h.logWriteRejection(r, "origin")
				httpapi.WriteError(w, r, http.StatusForbidden,
					"origin_rejected", "management request origin could not be verified")
				return
			}
			if !h.sessions.ValidateCSRF(token, r.Header.Get("X-CSRF-Token")) {
				h.logWriteRejection(r, "csrf")
				httpapi.WriteError(w, r, http.StatusForbidden,
					"csrf_rejected", "management CSRF token is invalid")
				return
			}
		}
		next(w, r)
	}
}

func (h *Handler) preparePublicWrite(w http.ResponseWriter, r *http.Request,
	limiter *adminauth.Limiter) bool {
	if !h.transport.AllowWrite(r) {
		httpapi.WriteError(w, r, http.StatusForbidden,
			"https_required", "HTTPS is required for management changes")
		return false
	}
	if !h.transport.SameOrigin(r) {
		h.logWriteRejection(r, "origin")
		httpapi.WriteError(w, r, http.StatusForbidden,
			"origin_rejected", "management request origin could not be verified")
		return false
	}
	providedCSRF := r.Header.Get("X-CSRF-Token")
	if len(providedCSRF) != len(h.publicCSRF) ||
		subtle.ConstantTimeCompare([]byte(providedCSRF), []byte(h.publicCSRF)) != 1 {
		h.logWriteRejection(r, "csrf")
		httpapi.WriteError(w, r, http.StatusForbidden,
			"csrf_rejected", "management CSRF token is invalid")
		return false
	}
	if !limiter.Allow() {
		w.Header().Set("Retry-After", "60")
		httpapi.WriteError(w, r, http.StatusTooManyRequests,
			"rate_limited", "too many authentication attempts")
		return false
	}
	return true
}

func (h *Handler) preparePublicWriteWithoutOrigin(w http.ResponseWriter, r *http.Request,
	limiter *adminauth.Limiter) bool {
	if !h.transport.AllowWrite(r) {
		httpapi.WriteError(w, r, http.StatusForbidden,
			"https_required", "HTTPS is required for management changes")
		return false
	}
	providedCSRF := r.Header.Get("X-CSRF-Token")
	if len(providedCSRF) != len(h.publicCSRF) ||
		subtle.ConstantTimeCompare([]byte(providedCSRF), []byte(h.publicCSRF)) != 1 {
		h.logWriteRejection(r, "csrf")
		httpapi.WriteError(w, r, http.StatusForbidden,
			"csrf_rejected", "management CSRF token is invalid")
		return false
	}
	if !limiter.Allow() {
		w.Header().Set("Retry-After", "60")
		httpapi.WriteError(w, r, http.StatusTooManyRequests,
			"rate_limited", "too many authentication attempts")
		return false
	}
	return true
}

func (h *Handler) requireRequestOrigin(w http.ResponseWriter, r *http.Request) bool {
	if h.transport.SameOrigin(r) {
		return true
	}
	h.logWriteRejection(r, "origin")
	httpapi.WriteError(w, r, http.StatusForbidden,
		"origin_rejected", "management request origin could not be verified")
	return false
}

func (h *Handler) logWriteRejection(r *http.Request, category string) {
	h.logger.Warn("management write rejected",
		"category", category, "request_id", httpapi.RequestID(r.Context()))
}

func (h *Handler) sessionFromRequest(r *http.Request) (string, string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", "", false
	}
	csrf, ok := h.sessions.Validate(cookie.Value)
	return cookie.Value, csrf, ok
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/admin/",
		MaxAge: int((12 * time.Hour) / time.Second), HttpOnly: true,
		Secure: h.transport.Secure(r), SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/admin/", MaxAge: -1,
		HttpOnly: true, Secure: h.transport.Secure(r), SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) loadState(w http.ResponseWriter, r *http.Request) (state.State, bool) {
	value, err := h.store.Load()
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_unavailable", "application state is unavailable")
		return state.State{}, false
	}
	return value, true
}

func (h *Handler) testCandidate(r *http.Request, value state.State,
	testLocation *testLocationInput) (weather.Verification, string, error) {
	select {
	case h.testSlot <- struct{}{}:
		defer func() { <-h.testSlot }()
	default:
		return weather.Verification{}, "", errQWeatherTestBusy
	}
	if !h.testLimit.Allow() {
		return weather.Verification{}, "", errQWeatherTestRateLimited
	}
	testContext, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if testLocation == nil || testLocation.Latitude == nil || testLocation.Longitude == nil {
		return weather.Verification{}, "", location.ErrRequired
	}
	point := &location.Point{
		Latitude: *testLocation.Latitude, Longitude: *testLocation.Longitude,
		City: testLocation.City, Region: testLocation.Region,
		Country: testLocation.Country, Timezone: testLocation.Timezone,
	}
	verification, fingerprint, err := h.runtime.Test(testContext, value, point)
	if err != nil {
		h.logger.Warn("QWeather configuration test failed", "error", err)
	}
	return verification, fingerprint, err
}

func (h *Handler) writeTestError(w http.ResponseWriter, r *http.Request, err error, retained bool) {
	status := http.StatusBadGateway
	code := "qweather_test_failed"
	message := "QWeather connection test failed"
	if errors.Is(err, location.ErrRequired) {
		status = http.StatusUnprocessableEntity
		code = "test_location_unavailable"
		message = "A temporary browser location is required for QWeather verification"
	} else if errors.Is(err, location.ErrInvalid) {
		status = http.StatusUnprocessableEntity
		code = "invalid_test_location"
		message = "The temporary browser location is invalid"
	} else if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		code = "qweather_timeout"
		message = "QWeather connection test timed out"
	} else if errors.Is(err, errQWeatherTestBusy) {
		status = http.StatusTooManyRequests
		code = "qweather_test_busy"
		message = "Another QWeather connection test is already running"
		w.Header().Set("Retry-After", "1")
	} else if errors.Is(err, errQWeatherTestRateLimited) {
		status = http.StatusTooManyRequests
		code = "qweather_test_rate_limited"
		message = "Too many QWeather connection tests; try again later"
		w.Header().Set("Retry-After", "60")
	} else {
		var upstream *qweather.UpstreamError
		if errors.As(err, &upstream) {
			switch {
			case upstream.HTTPStatus == http.StatusUnauthorized || upstream.HTTPStatus == http.StatusForbidden ||
				upstream.Code == "401" || upstream.Code == "403":
				code = "qweather_credentials_rejected"
				message = "QWeather rejected the credential; check the Project ID, Credential ID, API Host, and private key"
			case upstream.HTTPStatus == http.StatusTooManyRequests || upstream.Code == "429":
				status = http.StatusTooManyRequests
				code = "qweather_rate_limited"
				message = "QWeather request limit was reached; try again later"
				if upstream.Delay > 0 {
					seconds := (upstream.Delay + time.Second - 1) / time.Second
					w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
				}
			case upstream.HTTPStatus >= 500 || strings.HasPrefix(upstream.Code, "5"):
				code = "qweather_unavailable"
				message = "QWeather is temporarily unavailable"
			}
		}
	}
	if retained {
		message += "; existing settings were retained"
	}
	httpapi.WriteError(w, r, status, code, message)
}

func (h *Handler) saveAndActivate(w http.ResponseWriter, r *http.Request,
	candidate state.State, prepared platform.PreparedChange) bool {
	writeResult, err := h.store.Save(candidate)
	if err != nil {
		httpapi.WriteError(w, r, http.StatusInternalServerError,
			"state_write_failed", "could not save application state")
		return false
	}
	h.reportWriteResult(w, r, writeResult)
	prepared.Activate()
	return true
}

func (h *Handler) reportWriteResult(w http.ResponseWriter, r *http.Request, result state.WriteResult) {
	if result.DurabilityConfirmed {
		return
	}
	w.Header().Set(stateWarningHeader, stateWarningValue)
	h.logger.Error("state durability could not be confirmed",
		"request_id", httpapi.RequestID(r.Context()), "error", result.DurabilityWarning)
}

func (h *Handler) acquirePasswordSlot(w http.ResponseWriter, r *http.Request) bool {
	select {
	case h.passwordSlots <- struct{}{}:
		return true
	default:
		httpapi.WriteError(w, r, http.StatusTooManyRequests,
			"authentication_busy", "authentication is temporarily busy")
		return false
	}
}

func (h *Handler) releasePasswordSlot() { <-h.passwordSlots }

func qweatherState(input qweatherInput, existingKey string) state.QWeatherState {
	privateKey := strings.TrimSpace(input.PrivateKeyPEM)
	if privateKey == "" {
		privateKey = existingKey
	}
	return state.QWeatherState{
		APIHost:       strings.ToLower(strings.TrimSpace(input.APIHost)),
		ProjectID:     strings.TrimSpace(input.ProjectID),
		CredentialID:  strings.TrimSpace(input.CredentialID),
		PrivateKeyPEM: privateKey,
		Language:      "zh", Unit: "m", RequestTimeoutSeconds: 10,
		CircuitCooldownSeconds: int64((15 * time.Minute) / time.Second),
	}
}

func newDeviceToken(name string, now time.Time) (string, state.DeviceToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return "", state.DeviceToken{}, errors.New("device name must contain 1-64 characters")
	}
	tokenValue, err := randomValue()
	if err != nil {
		return "", state.DeviceToken{}, err
	}
	var idBytes [9]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", state.DeviceToken{}, err
	}
	raw := "mt_" + tokenValue
	id := "device_" + base64.RawURLEncoding.EncodeToString(idBytes[:])
	return raw, state.DeviceToken{
		ID: id, Name: name, Hash: auth.HashToken(raw), CreatedAt: now.UTC(),
	}, nil
}

func randomValue() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func publicToken(token state.DeviceToken) map[string]any {
	return map[string]any{
		"id": token.ID, "name": token.Name, "created_at": token.CreatedAt,
	}
}

func publicOrigins(origins []string) []map[string]string {
	result := make([]map[string]string, 0, len(origins))
	for _, origin := range origins {
		result = append(result, publicOrigin(origin))
	}
	return result
}

func publicOrigin(origin string) map[string]string {
	return map[string]string{"id": adminauth.PublicOriginID(origin), "origin": origin}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maximumJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		httpapi.WriteError(w, r, http.StatusBadRequest, "invalid_json", "request body is invalid")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		httpapi.WriteError(w, r, http.StatusBadRequest, "invalid_json", "request body has trailing data")
		return false
	}
	return true
}

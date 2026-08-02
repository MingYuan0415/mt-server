// Package auth authenticates device API requests.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/MingYuan0415/mt-server/internal/platform/httpapi"
)

type principalKey struct{}

// Principal identifies the authenticated caller.
type Principal struct {
	DeviceID string
}

// Credential is one named device-token verifier.
type Credential struct {
	DeviceID string
	Hash     [sha256.Size]byte
}

// CredentialFromHex decodes a persistent SHA-256 verifier.
func CredentialFromHex(deviceID, value string) (Credential, bool) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || deviceID == "" {
		return Credential{}, false
	}
	var hash [sha256.Size]byte
	copy(hash[:], decoded)
	return Credential{DeviceID: deviceID, Hash: hash}, true
}

// HashToken returns the persistent verifier for a high-entropy token.
func HashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// Middleware authenticates a bounded collection of device tokens.
type Middleware struct {
	credentials []Credential
}

// New constructs middleware for a single token.
func New(token string) *Middleware {
	digest := sha256.Sum256([]byte(token))
	return NewCredentials([]Credential{{DeviceID: "default", Hash: digest}})
}

// NewCredentials constructs middleware from persistent verifiers.
func NewCredentials(credentials []Credential) *Middleware {
	return &Middleware{credentials: append([]Credential(nil), credentials...)}
}

// Wrap rejects requests without a matching Bearer token.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w, r)
			return
		}

		candidateHash := sha256.Sum256([]byte(token))
		matched := 0
		deviceID := ""
		for _, credential := range m.credentials {
			equal := subtle.ConstantTimeCompare(candidateHash[:], credential.Hash[:])
			if equal == 1 {
				deviceID = credential.DeviceID
			}
			matched |= equal
		}
		if matched != 1 {
			unauthorized(w, r)
			return
		}

		principal := Principal{DeviceID: deviceID}
		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// PrincipalFromContext returns the authenticated device identity.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

func bearerToken(value string) (string, bool) {
	if len(value) > 512 {
		return "", false
	}
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpapi.WriteError(w, r, http.StatusUnauthorized,
		"unauthorized", "authentication required")
}

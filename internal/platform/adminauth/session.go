package adminauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"time"
)

const (
	sessionLifetime = 12 * time.Hour
	sessionIdle     = time.Hour
	maximumSessions = 64
)

type session struct {
	csrf      string
	createdAt time.Time
	lastSeen  time.Time
}

// Sessions owns restart-local management sessions.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]session
	now      func() time.Time
}

// NewSessions constructs an empty session manager.
func NewSessions() *Sessions {
	return &Sessions{sessions: make(map[string]session), now: time.Now}
}

// Create returns a raw session cookie and its CSRF value.
func (s *Sessions) Create() (string, string, error) {
	token, err := randomToken()
	if err != nil {
		return "", "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	for len(s.sessions) >= maximumSessions {
		s.removeOldestLocked()
	}
	s.sessions[tokenKey(token)] = session{csrf: csrf, createdAt: now, lastSeen: now}
	return token, csrf, nil
}

// Validate checks a cookie and refreshes its idle time.
func (s *Sessions) Validate(token string) (string, bool) {
	if len(token) > 128 {
		return "", false
	}
	now := s.now().UTC()
	key := tokenKey(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[key]
	if !ok || expired(value, now) {
		delete(s.sessions, key)
		return "", false
	}
	value.lastSeen = now
	s.sessions[key] = value
	return value.csrf, true
}

// ValidateCSRF checks both the session and supplied CSRF token.
func (s *Sessions) ValidateCSRF(token, csrf string) bool {
	expected, ok := s.Validate(token)
	if !ok || len(csrf) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(csrf), []byte(expected)) == 1
}

// Delete removes a management session.
func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, tokenKey(token))
	s.mu.Unlock()
}

// Clear invalidates every management session.
func (s *Sessions) Clear() {
	s.mu.Lock()
	clear(s.sessions)
	s.mu.Unlock()
}

func (s *Sessions) purgeLocked(now time.Time) {
	for key, value := range s.sessions {
		if expired(value, now) {
			delete(s.sessions, key)
		}
	}
}

func (s *Sessions) removeOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, value := range s.sessions {
		if oldestKey == "" || value.lastSeen.Before(oldest) {
			oldestKey = key
			oldest = value.lastSeen
		}
	}
	delete(s.sessions, oldestKey)
}

func expired(value session, now time.Time) bool {
	return now.Sub(value.createdAt) >= sessionLifetime || now.Sub(value.lastSeen) >= sessionIdle
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func tokenKey(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

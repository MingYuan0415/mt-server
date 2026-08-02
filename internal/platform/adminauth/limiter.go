package adminauth

import (
	"sync"
	"time"
)

// Limiter is a deliberately global, privacy-preserving authentication limiter.
type Limiter struct {
	mu          sync.Mutex
	windowStart time.Time
	attempts    int
	maximum     int
	window      time.Duration
	now         func() time.Time
}

// NewLimiter permits maximum attempts per rolling window.
func NewLimiter(maximum int, window time.Duration) *Limiter {
	return &Limiter{maximum: maximum, window: window, now: time.Now}
}

// Allow consumes one attempt when capacity remains.
func (l *Limiter) Allow() bool {
	now := l.now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windowStart.IsZero() || now.Sub(l.windowStart) >= l.window {
		l.windowStart = now
		l.attempts = 0
	}
	if l.attempts >= l.maximum {
		return false
	}
	l.attempts++
	return true
}

// Reset clears failures after successful authentication.
func (l *Limiter) Reset() {
	l.mu.Lock()
	l.windowStart = time.Time{}
	l.attempts = 0
	l.mu.Unlock()
}

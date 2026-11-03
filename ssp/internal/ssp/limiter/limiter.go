// Package limiter implements a small in-process token bucket used for Supply
// and Client API quotas.
package limiter

import (
	"sync"
	"time"
)

type bucket struct {
	tokens   float64
	lastFill time.Time
}

// Limiter applies one token bucket per key.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

// New returns an empty limiter.
func New() *Limiter {
	return &Limiter{buckets: make(map[string]*bucket), now: func() time.Time { return time.Now().UTC() }}
}

// Allow consumes one token when the key's configured rate permits it. A rate
// of zero or below disables limiting for that key.
func (l *Limiter) Allow(key string, rate int) bool {
	if rate <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: float64(rate) - 1, lastFill: now}
		l.pruneLocked(now)
		return true
	}
	elapsed := now.Sub(entry.lastFill).Seconds()
	if elapsed > 0 {
		entry.tokens += elapsed * float64(rate)
		if entry.tokens > float64(rate) {
			entry.tokens = float64(rate)
		}
		entry.lastFill = now
	}
	if entry.tokens < 1 {
		return false
	}
	entry.tokens--
	return true
}

func (l *Limiter) pruneLocked(now time.Time) {
	if len(l.buckets) < 4096 {
		return
	}
	for key, entry := range l.buckets {
		if now.Sub(entry.lastFill) > 10*time.Minute {
			delete(l.buckets, key)
		}
	}
}

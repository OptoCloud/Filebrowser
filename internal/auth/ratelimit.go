package auth

import (
	"sync"
	"time"
)

// Backoff tracks failures per key and imposes exponentially growing waits
// after a number of free attempts. There is no hard lockout: the wait is capped.
type Backoff struct {
	Free int
	Base time.Duration
	Max  time.Duration
	// Forget resets a key after this long without failures.
	Forget time.Duration

	mu      sync.Mutex
	entries map[string]*backoffEntry
	lastGC  time.Time
}

type backoffEntry struct {
	fails int
	until time.Time
	last  time.Time
}

func NewBackoff(free int, base, max, forget time.Duration) *Backoff {
	return &Backoff{Free: free, Base: base, Max: max, Forget: forget, entries: map[string]*backoffEntry{}}
}

// Wait returns how long the key must wait before another attempt (0 = allowed).
func (b *Backoff) Wait(key string, now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[key]
	if e == nil || now.After(e.until) {
		return 0
	}
	return e.until.Sub(now)
}

// Fail records a failure and returns the new failure count.
func (b *Backoff) Fail(key string, now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gc(now)
	e := b.entries[key]
	if e == nil || now.Sub(e.last) > b.Forget {
		e = &backoffEntry{}
		b.entries[key] = e
	}
	e.fails++
	e.last = now
	if over := e.fails - b.Free; over > 0 {
		d := b.Base << min(over-1, 30)
		if d > b.Max || d <= 0 {
			d = b.Max
		}
		e.until = now.Add(d)
	}
	return e.fails
}

func (b *Backoff) Reset(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key)
}

func (b *Backoff) gc(now time.Time) {
	if now.Sub(b.lastGC) < time.Minute {
		return
	}
	b.lastGC = now
	for k, e := range b.entries {
		if now.Sub(e.last) > b.Forget && now.After(e.until) {
			delete(b.entries, k)
		}
	}
}

// RateLimiter is a token bucket per key.
type RateLimiter struct {
	Rate  float64 // tokens per second
	Burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewRateLimiter(perMinute, burst int) *RateLimiter {
	return &RateLimiter{Rate: float64(perMinute) / 60, Burst: float64(burst), buckets: map[string]*bucket{}}
}

// Allow consumes one token for key if available.
func (r *RateLimiter) Allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.lastGC) > time.Minute {
		r.lastGC = now
		for k, b := range r.buckets {
			if b.tokens+now.Sub(b.last).Seconds()*r.Rate >= r.Burst {
				delete(r.buckets, k)
			}
		}
	}
	b := r.buckets[key]
	if b == nil {
		b = &bucket{tokens: r.Burst, last: now}
		r.buckets[key] = b
	}
	b.tokens = min(r.Burst, b.tokens+now.Sub(b.last).Seconds()*r.Rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a simple per-key sliding window counter.
type Limiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	buckets map[string]*bucket
}

type bucket struct {
	count int
	start time.Time
}

func New(limit int, window time.Duration) *Limiter {
	if limit <= 0 {
		limit = 60
	}
	if window <= 0 {
		window = time.Minute
	}
	return &Limiter{
		window:  window,
		limit:   limit,
		buckets: make(map[string]*bucket),
	}
}

// Allow returns true if under limit.
func (l *Limiter) Allow(key string) bool {
	if key == "" {
		key = "anonymous"
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || now.Sub(b.start) >= l.window {
		l.buckets[key] = &bucket{count: 1, start: now}
		// opportunistic cleanup
		if len(l.buckets) > 5000 {
			for k, v := range l.buckets {
				if now.Sub(v.start) >= l.window {
					delete(l.buckets, k)
				}
			}
		}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}

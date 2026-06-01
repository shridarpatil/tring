package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fixedWindowLimiter is a tiny in-memory, per-key fixed-window rate limiter.
// It blunts abuse of public endpoints without pulling in Redis. It is
// per-process — each API replica keeps its own window — which is acceptable
// here because the per-tenant concurrent-session cap (DB-backed, global) is
// the real backstop; this just stops a single host hammering one replica.
type fixedWindowLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*windowCount
}

type windowCount struct {
	count int
	reset time.Time
}

func newFixedWindowLimiter(limit int, window time.Duration) *fixedWindowLimiter {
	return &fixedWindowLimiter{limit: limit, window: window, hits: map[string]*windowCount{}}
}

// allow records a hit for key and reports whether it's within the limit for
// the current window. now is passed in for deterministic tests.
func (l *fixedWindowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	wc := l.hits[key]
	if wc == nil || now.After(wc.reset) {
		// Opportunistic GC so the map can't grow without bound under churn.
		if len(l.hits) > 10000 {
			for k, v := range l.hits {
				if now.After(v.reset) {
					delete(l.hits, k)
				}
			}
		}
		l.hits[key] = &windowCount{count: 1, reset: now.Add(l.window)}
		return true
	}
	wc.count++
	return wc.count <= l.limit
}

// rateLimitByIP returns middleware that caps requests per client IP per
// window, replying 429 + Retry-After when exceeded. One limiter is created
// per call, so scope it to the route(s) you want throttled.
func rateLimitByIP(limit int, window time.Duration) func(http.Handler) http.Handler {
	lim := newFixedWindowLimiter(limit, window)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !lim.allow(clientIP(r), time.Now()) {
				w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the client IP for rate-limiting. It prefers the leftmost
// X-Forwarded-For / X-Real-IP (set by a trusted proxy/CDN in production) and
// falls back to the TCP peer.
//
// SECURITY: X-Forwarded-For is client-spoofable unless your edge overwrites
// inbound values. Deploy behind a proxy/WAF that strips client-supplied XFF,
// or an attacker can rotate fake IPs to evade the per-IP limit — the
// per-tenant concurrent-session cap is the non-spoofable backstop.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

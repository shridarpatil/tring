package api

import (
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxRequestBodyBytes caps every request body on the public router. The JSON
// API's largest legitimate payload (an IVR flow definition) is far below it.
// The audio upload is the one exception — it enforces its own maxAudioBytes.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// limitBody caps request bodies at maxRequestBodyBytes. A declared
// Content-Length over the cap is refused up front with 413; a chunked body is
// wrapped in http.MaxBytesReader so the handler's decode fails at the cap
// instead of buffering an unbounded stream.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// POST /v1/audio carries a multipart file and caps itself
		// (handleUploadAudio); wrapping it here would cut it at 1 MiB.
		if r.Method == http.MethodPost && r.URL.Path == "/v1/audio" {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > maxRequestBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets the headers every response gets: no MIME sniffing (so
// a JSON error or an audio file can never be reinterpreted as HTML/script),
// and no Referer leaking console URLs to third parties.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// adminCSP is the Content-Security-Policy for the embedded admin console.
// Checked against the Vite build: one external module script + stylesheet, no
// inline scripts or eval, fonts self-hosted (plus a data: font and data: SVGs
// in CSS), recordings/previews played from blob: URLs. Inline styles stay
// allowed — Vue :style bindings and vue-flow set them. The supervisor monitor
// opens a WebSocket to the gateway (gateway_public_url), so its ws/wss origin
// is added to connect-src.
func adminCSP(gatewayPublicURL string) string {
	connect := "'self'"
	if o := wsOrigin(gatewayPublicURL); o != "" {
		connect += " " + o
	}
	return "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' blob:; " +
		"connect-src " + connect + "; object-src 'none'; base-uri 'none'; " +
		"form-action 'self'; frame-ancestors 'none'"
}

// wsOrigin turns the gateway base URL into the WebSocket origin the console
// dials (supervisor.ts swaps http→ws). "" when it isn't an absolute URL or the
// host carries characters that could break out of the CSP directive.
func wsOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || strings.ContainsAny(u.Host, " ;,'\"") {
		return ""
	}
	switch u.Scheme {
	case "http", "ws":
		return "ws://" + u.Host
	case "https", "wss":
		return "wss://" + u.Host
	}
	return ""
}

// adminHeaders adds the console-only headers: the CSP above and a
// belt-and-braces X-Frame-Options for browsers that ignore frame-ancestors.
// Not applied to /sdk/* (embedded on customer sites) or the JSON API.
func adminHeaders(csp string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Frame-Options", "DENY")
			next.ServeHTTP(w, r)
		})
	}
}

// sdkHandler serves the embedded widget bundles. Directories 404 rather than
// getting http.FileServer's auto-generated listing. Expects the /sdk/ prefix
// already stripped.
func sdkHandler(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || strings.HasSuffix(name, "/") {
			http.NotFound(w, r)
			return
		}
		if st, err := fs.Stat(fsys, name); err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// Console login throttling: each attempt costs a ~70ms bcrypt, so without a
// cap the endpoint is both a password-guessing oracle and a CPU sink.
const (
	loginAttemptLimit  = 10
	loginAttemptWindow = 5 * time.Minute
	loginLimiterKeys   = 10000
)

// attemptLimiter is a fixed-window counter per key (client IP or email),
// in-process only: a multi-replica deployment gets a limit per replica, which
// still bounds guessing to replicas × limit per window.
type attemptLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	m       map[string]*attemptWindow
}

type attemptWindow struct {
	start time.Time
	n     int
}

func newAttemptLimiter(limit int, window time.Duration, maxKeys int) *attemptLimiter {
	return &attemptLimiter{
		limit:   limit,
		window:  window,
		maxKeys: maxKeys,
		now:     time.Now,
		m:       make(map[string]*attemptWindow),
	}
}

// take consumes one attempt for key. When the window is exhausted it returns
// false and how long until it resets. Counting on entry (not on failure) means
// a burst of parallel guesses can't all slip past before any is recorded.
func (l *attemptLimiter) take(key string) (time.Duration, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.m[key]
	if w == nil || now.Sub(w.start) >= l.window {
		if w == nil && len(l.m) >= l.maxKeys {
			l.evictLocked(now)
		}
		w = &attemptWindow{start: now}
		l.m[key] = w
	}
	if w.n >= l.limit {
		return w.start.Add(l.window).Sub(now), false
	}
	w.n++
	return 0, true
}

// release refunds one attempt — a successful login shouldn't count against a
// shared office IP.
func (l *attemptLimiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.m[key]; w != nil && w.n > 0 {
		w.n--
	}
}

// evictLocked bounds memory: drop expired windows, and if the table is still
// full (a key-spraying flood), drop the oldest window.
func (l *attemptLimiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, w := range l.m {
		if now.Sub(w.start) >= l.window {
			delete(l.m, k)
			continue
		}
		if oldestKey == "" || w.start.Before(oldest) {
			oldestKey, oldest = k, w.start
		}
	}
	if len(l.m) >= l.maxKeys && oldestKey != "" {
		delete(l.m, oldestKey)
	}
}

// clientIP is the limiter key for a request: realIP has already replaced
// RemoteAddr with the trusted client address; strip any port.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// writeTooManyRequests sends a 429 with Retry-After rounded up to a second.
func writeTooManyRequests(w http.ResponseWriter, retry time.Duration) {
	secs := int((retry + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
}

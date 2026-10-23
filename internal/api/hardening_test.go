package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// An oversized body on the unauthenticated login route is refused before the
// handler (and its DB lookup) ever runs.
func TestLoginRejectsOversizedBody(t *testing.T) {
	s := NewServer(Config{JWTSecret: "test-secret"})
	body := `{"email":"user@example.com","password":"` + strings.Repeat("a", maxRequestBodyBytes) + `"}`

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/console/login", strings.NewReader(body)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized login = %d, want 413", rec.Code)
	}
}

// Without a Content-Length the cap is enforced while reading: the handler's
// decode fails instead of buffering the whole stream.
func TestLimitBodyChunked(t *testing.T) {
	var readErr error
	h := limitBody(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.Copy(io.Discard, r.Body)
	}))
	req := httptest.NewRequest("POST", "/v1/flows", io.MultiReader(bytes.NewReader(make([]byte, maxRequestBodyBytes+1))))
	req.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), req)
	if readErr == nil {
		t.Fatal("reading an over-limit chunked body succeeded, want a MaxBytesError")
	}
}

// The audio upload enforces its own (larger) cap, so the router-wide 1 MiB
// limit must not apply to it.
func TestLimitBodyExemptsAudioUpload(t *testing.T) {
	var n int64
	h := limitBody(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		n, _ = io.Copy(io.Discard, r.Body)
	}))
	size := 3 << 20
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/audio", bytes.NewReader(make([]byte, size))))
	if rec.Code != http.StatusOK || n != int64(size) {
		t.Fatalf("audio upload: code=%d read=%d, want 200 and %d bytes", rec.Code, n, size)
	}
}

func TestAttemptLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newAttemptLimiter(3, time.Minute, 100)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if _, ok := l.take("a"); !ok {
			t.Fatalf("attempt %d refused, want allowed", i+1)
		}
	}
	retry, ok := l.take("a")
	if ok {
		t.Fatal("4th attempt allowed, want refused")
	}
	if retry <= 0 || retry > time.Minute {
		t.Fatalf("retry-after = %v, want within the window", retry)
	}
	if _, ok := l.take("b"); !ok {
		t.Fatal("other key refused, want independent buckets")
	}

	// A refund (successful login) gives the attempt back.
	l.release("a")
	if _, ok := l.take("a"); !ok {
		t.Fatal("attempt after release refused")
	}

	now = now.Add(time.Minute)
	if _, ok := l.take("a"); !ok {
		t.Fatal("attempt after the window refused, want reset")
	}
}

// The key table is bounded: once full, expired entries go first, then the
// oldest window — memory never grows past maxKeys.
func TestAttemptLimiterBoundsMemory(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newAttemptLimiter(1, time.Minute, 4)
	l.now = func() time.Time { return now }
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		l.take(strings.Repeat("k", i+1))
	}
	if len(l.m) > 4 {
		t.Fatalf("limiter holds %d keys, want <= 4", len(l.m))
	}
	// The most recent key is still being limited.
	if _, ok := l.take(strings.Repeat("k", 10)); ok {
		t.Fatal("newest key lost its count")
	}
}

func loginReq(ip, email string) *http.Request {
	req := httptest.NewRequest("POST", "/v1/console/login",
		strings.NewReader(`{"email":"`+email+`","password":"x"}`))
	req.RemoteAddr = ip + ":40000"
	return req
}

// Exhausted IP or email buckets short-circuit the login with 429 before any
// bcrypt/DB work. (The allowed path needs a DB; the limiter itself is covered
// by TestAttemptLimiter.)
func TestLoginRateLimited(t *testing.T) {
	s := NewServer(Config{JWTSecret: "test-secret"})
	r := s.Router()

	for i := 0; i < loginAttemptLimit; i++ {
		s.loginByIP.take("198.51.100.7")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, loginReq("198.51.100.7", "user@example.com"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP over limit = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}

	for i := 0; i < loginAttemptLimit; i++ {
		s.loginByEmail.take("victim@example.com")
	}
	rec = httptest.NewRecorder()
	// Email keys are case-insensitive, so the casing trick doesn't reset it.
	r.ServeHTTP(rec, loginReq("203.0.113.9", "Victim@Example.com"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("email over limit = %d, want 429", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := NewServer(Config{JWTSecret: "test-secret", GatewayPublicURL: "https://gw.example.com"})
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/tenants/me", nil))
	h := rec.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("/v1 X-Content-Type-Options = %q", h.Get("X-Content-Type-Options"))
	}
	if h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("/v1 Referrer-Policy = %q", h.Get("Referrer-Policy"))
	}
	// The JSON API carries no CSP (it isn't a document) — and CORS still works.
	if h.Get("Content-Security-Policy") != "" {
		t.Errorf("/v1 unexpectedly has a CSP")
	}

	admin := adminHeaders(adminCSP("https://gw.example.com"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/", nil))
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("/admin X-Frame-Options = %q", rec.Header().Get("X-Frame-Options"))
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"frame-ancestors 'none'", "script-src 'self'", "connect-src 'self' wss://gw.example.com", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("/admin CSP %q missing %q", csp, want)
		}
	}
}

func TestAdminCSPGatewayOrigin(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8087":        "connect-src 'self' ws://localhost:8087;",
		"https://gw.example.com/base/": "connect-src 'self' wss://gw.example.com;",
		"wss://gw.example.com":         "connect-src 'self' wss://gw.example.com;",
		"":                             "connect-src 'self';",
		"https://evil; script-src *":   "connect-src 'self';",
		"/relative/only":               "connect-src 'self';",
	}
	for in, want := range cases {
		if got := adminCSP(in); !strings.Contains(got, want) {
			t.Errorf("adminCSP(%q) = %q, want it to contain %q", in, got, want)
		}
	}
}

// /sdk/ serves the bundles but never a directory listing.
func TestSDKHandlerNoDirectoryListing(t *testing.T) {
	fsys := fstest.MapFS{
		"voice.js":    {Data: []byte("x")},
		"chunks/a.js": {Data: []byte("y")},
	}
	h := http.StripPrefix("/sdk/", sdkHandler(fsys))
	for path, want := range map[string]int{
		"/sdk/voice.js":    http.StatusOK,
		"/sdk/chunks/a.js": http.StatusOK,
		"/sdk/":            http.StatusNotFound,
		"/sdk/chunks/":     http.StatusNotFound,
		"/sdk/chunks":      http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

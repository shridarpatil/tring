package api

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/events"
)

// TestStaticAssetsGzipped verifies the embedded static mounts (/sdk/* and
// /admin/*) are served gzip-compressed when the client advertises it. The
// bundles ship uncompressed on disk, so without this the API sends ~2.5x the
// bytes for every JS/CSS asset. Needs the bundles built (`make sdk` +
// `npm run build`); skips the route if a mount isn't present.
func TestStaticAssetsGzipped(t *testing.T) {
	s := NewServer(Config{JWTSecret: "test-secret"})
	r := s.Router()

	// /sdk/voice.js has a stable (unhashed) name, so we can request it directly.
	for _, path := range []string{"/sdk/voice.js", "/admin/"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (built the bundles?)", path, rec.Code)
		}
		if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
			t.Fatalf("GET %s Content-Encoding = %q, want gzip", path, enc)
		}
		// The compressed body must still decode to non-empty content.
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("GET %s: gzip.NewReader: %v", path, err)
		}
		body, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("GET %s: reading gzip body: %v", path, err)
		}
		if len(body) == 0 {
			t.Fatalf("GET %s: decoded body is empty", path)
		}
	}
}

// TestStaticAssetsNotGzippedWithoutAcceptEncoding verifies we don't force
// compression on clients that didn't ask for it.
func TestStaticAssetsNotGzippedWithoutAcceptEncoding(t *testing.T) {
	s := NewServer(Config{JWTSecret: "test-secret"})
	r := s.Router()

	req := httptest.NewRequest("GET", "/sdk/voice.js", nil)
	req.Header.Set("Accept-Encoding", "identity")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /sdk/voice.js = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); strings.Contains(enc, "gzip") {
		t.Fatalf("Content-Encoding = %q, want no gzip when not accepted", enc)
	}
}

// TestV1JSONGzipped verifies the /v1 JSON list endpoints are gzip-compressed
// when the client advertises it. DB-gated (skips without TRING_TEST_DSN).
func TestV1JSONGzipped(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutboundForHandlerTest(t, f, "+918001234567", true)
	s := newTestServer(f)

	req := authedRequest(t, s, f, "GET", "/v1/numbers", "")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/numbers = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("reading gzip body: %v", err)
	}
	// The decoded payload must be the real JSON, not garbage — the inserted
	// number's E.164 proves gzip round-tripped actual content.
	if !strings.Contains(string(body), "+918001234567") {
		t.Fatalf("decoded gzip body missing expected content: %s", body)
	}
}

// TestV1SSEStreamNotGzipped is the guardrail that keeps the compression scope
// off the live dashboard stream: gzip-buffering an SSE response would stall
// the event frames. The stream sits outside the compressed group, so even
// with Accept-Encoding: gzip it must come back uncompressed. DB-gated.
func TestV1SSEStreamNotGzipped(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	s.hub = events.NewTestHub() // handler calls hub.Subscribe; nil would panic

	req := authedRequest(t, s, f, "GET", "/v1/stream/dashboard", "")
	req.Header.Set("Accept-Encoding", "gzip")
	// The stream blocks until the request context ends; give it a short
	// lifetime so ServeHTTP returns after the snapshot frame is written.
	ctx, cancel := context.WithTimeout(req.Context(), 150*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if enc := rec.Header().Get("Content-Encoding"); strings.Contains(enc, "gzip") {
		t.Fatalf("SSE stream Content-Encoding = %q, want uncompressed", enc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q, want text/event-stream (did the handler run?)", ct)
	}
	// Sanity: the snapshot frame really was emitted (proves we exercised the
	// stream path, not an auth/error short-circuit).
	if !strings.Contains(rec.Body.String(), "event: snapshot") {
		t.Fatalf("no snapshot frame in SSE body:\n%s", rec.Body.String())
	}
}

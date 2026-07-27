package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

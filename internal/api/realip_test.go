package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveRealIP runs one request through realIP(trusted) and reports the
// r.RemoteAddr the wrapped handler observed.
func serveRealIP(trusted []string, remoteAddr string, headers map[string]string) string {
	var got string
	h := realIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	}))

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// TestRealIPNoTrustedProxies is the regression guard for the chi
// middleware.RealIP spoofing advisories (GHSA-3fxj-6jh8-hvhx and friends):
// with no proxies configured, a direct caller must NOT be able to dictate the
// address we log, whichever header it tries.
func TestRealIPNoTrustedProxies(t *testing.T) {
	const direct = "203.0.113.9:44321"

	for _, hdr := range []string{"X-Forwarded-For", "X-Real-IP", "True-Client-IP"} {
		got := serveRealIP(nil, direct, map[string]string{hdr: "1.2.3.4"})
		if got != direct {
			t.Errorf("%s: RemoteAddr = %q, want %q (header must not be trusted)", hdr, got, direct)
		}
	}
}

// TestRealIPTrustedProxies covers the configured-proxy path: the XFF chain is
// walked right-to-left, trusted hops are skipped, and the first untrusted
// entry becomes the client.
func TestRealIPTrustedProxies(t *testing.T) {
	trusted := []string{"10.0.0.0/8"}

	tests := []struct {
		name string
		xff  string
		want string
	}{
		// Single trusted proxy in front: the one entry it appended is the client.
		{"one hop", "198.51.100.7", "198.51.100.7"},
		// Two trusted hops appended their peers; walk past the trusted 10.x
		// entry to the first address we didn't put there.
		{"skips trusted hops", "198.51.100.7, 10.0.0.5", "198.51.100.7"},
		// A client that pre-seeds XFF cannot push its forgery past the real
		// entry our proxy appended.
		{"client-supplied prefix ignored", "1.2.3.4, 198.51.100.7, 10.0.0.5", "198.51.100.7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serveRealIP(trusted, "10.0.0.5:9999", map[string]string{"X-Forwarded-For": tt.xff})
			if got != tt.want {
				t.Errorf("RemoteAddr = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRealIPInvalidCIDRPanics documents that a typo'd TRING_TRUSTED_PROXIES
// fails loudly at construction rather than silently trusting nothing.
func TestRealIPInvalidCIDRPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("realIP with an invalid CIDR did not panic")
		}
	}()
	realIP([]string{"not-a-cidr"})
}

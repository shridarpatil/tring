package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPprofServerLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:6060", "[::1]:6060", "localhost:6060"} {
		if _, err := newPprofServer(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{":6060", "0.0.0.0:6060", "10.0.0.5:6060", "example.com:6060", "6060"} {
		if _, err := newPprofServer(addr); err == nil {
			t.Errorf("%s: accepted, want loopback-only refusal", addr)
		}
	}
}

func TestPprofServerServesProfiles(t *testing.T) {
	srv, err := newPprofServer("127.0.0.1:6060")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutine"} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

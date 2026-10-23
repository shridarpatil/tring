package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// /fs/* returns agent SIP passwords, so it must live only on the loopback
// FSRouter — never on the public Router.
func TestFSDirectoryOnlyOnFSRouter(t *testing.T) {
	s := NewServer(Config{FSBasicAuth: "fs:0123456789abcdef"})

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest("POST", "/fs/directory", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("public Router /fs/directory = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/fs/directory", nil)
	req.SetBasicAuth("fs", "wrong")
	s.FSRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("FSRouter bad creds = %d, want 401", rec.Code)
	}
}

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// A console JWT outlives its user unless the middleware re-checks the user
// row: deleting a user must revoke their still-unexpired session.
func TestConsoleTokenForDeletedUserRejected(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	if rec := serve(t, s, f, http.MethodGet, "/v1/agents", ""); rec.Code != http.StatusOK {
		t.Fatalf("live user: status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	// Mint the token while the user exists, then delete the user.
	req := authedRequest(t, s, f, http.MethodGet, "/v1/agents", "")
	superviseReq := authedRequest(t, s, f, http.MethodPost, "/v1/calls/"+f.TenantID.String()+"/supervise", "")
	if _, err := f.DB.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, f.UserID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	for _, r := range []*http.Request{req, superviseReq} {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with deleted user's token: status = %d, want 401; body %s",
				r.Method, r.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

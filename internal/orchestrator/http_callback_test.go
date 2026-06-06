package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// http_callback on a 2xx stashes the JSON response into flow vars (so a later
// Transfer can route on them) and templates {{vars}} into the request.
func TestRunHTTPCallback2xxStashesVars(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"team":"sales","priority":true,"tier":2}`))
	}))
	defer srv.Close()

	e := &Executor{Logger: slog.Default(), HTTP: srv.Client(), queue: newCallQueue(), rr: map[string]int{}}
	st := &callState{
		uuid:     "u1",
		tenantID: uuid.New(),
		caller:   "+1555",
		vars:     map[string]string{"account": "A42"},
		log:      slog.Default(),
	}
	node := &ivrNode{ID: "h", Type: nodeHTTPCallback, Config: map[string]interface{}{
		"url":           srv.URL,
		"method":        "POST",
		"body_template": `{"caller":"{{caller}}","account":"{{account}}"}`,
	}}

	// conn is nil — stashResponse must tolerate it (no channel-var Set in tests).
	outcome, err := e.runHTTPCallback(context.Background(), nil, node, st)
	if err != nil {
		t.Fatalf("runHTTPCallback: %v", err)
	}
	if outcome != "http:2xx" {
		t.Fatalf("outcome = %q, want http:2xx", outcome)
	}
	if st.vars["team"] != "sales" || st.vars["priority"] != "true" || st.vars["tier"] != "2" {
		t.Fatalf("vars not stashed: %+v", st.vars)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("body not JSON: %q", gotBody)
	}
	if sent["caller"] != "+1555" || sent["account"] != "A42" {
		t.Fatalf("template substitution wrong: %+v", sent)
	}
}

// A non-2xx routes to the error edge without dropping the call or stashing vars.
func TestRunHTTPCallbackNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := &Executor{Logger: slog.Default(), HTTP: srv.Client(), queue: newCallQueue(), rr: map[string]int{}}
	st := &callState{uuid: "u1", tenantID: uuid.New(), vars: map[string]string{}, log: slog.Default()}
	node := &ivrNode{ID: "h", Type: nodeHTTPCallback, Config: map[string]interface{}{"url": srv.URL}}

	outcome, err := e.runHTTPCallback(context.Background(), nil, node, st)
	if err != nil {
		t.Fatalf("runHTTPCallback: %v", err)
	}
	if outcome != "http:non2xx" {
		t.Fatalf("outcome = %q, want http:non2xx", outcome)
	}
	if len(st.vars) != 0 {
		t.Fatalf("non-2xx should stash nothing, got %+v", st.vars)
	}
}

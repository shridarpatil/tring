package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func TestSdkTransferTargetsListsAgents(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	me := f.AddUser(t, "agent", "me@example.com")
	f.AddUser(t, "agent", "a@example.com")
	f.AddUser(t, "agent", "b@example.com")
	tok, _, err := s.IssueAgentToken(me, f.TenantID, "me@example.com", f.SIPDomain, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, s.Router(), "GET", "/v1/sdk/transfer-targets", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("targets: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Agents []struct {
			Sip string `json:"sip"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, a := range resp.Agents {
		got[a.Sip] = true
	}
	// Presence is off in the test server, so every available agent but the
	// caller is a target.
	if !got["a@example.com"] || !got["b@example.com"] || got["me@example.com"] {
		t.Fatalf("agents = %+v", resp.Agents)
	}
}

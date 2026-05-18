package api

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// JWT tests construct a bare *Server with just jwtSecret set — Issue/Parse
// don't touch the DB or FS client.
func newJWTTestServer(secret string) *Server {
	return &Server{jwtSecret: []byte(secret)}
}

func TestIssueAndParseRoundtrip(t *testing.T) {
	s := newJWTTestServer("topsecret")
	agentID, tenantID := uuid.New(), uuid.New()

	tok, exp, err := s.IssueAgentToken(agentID, tenantID, "alice", "tring.local", 5*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	if time.Until(exp) > 5*time.Minute+time.Second || time.Until(exp) < 4*time.Minute {
		t.Errorf("exp out of band: %v from now", time.Until(exp))
	}

	claims, err := s.ParseAgentToken(tok)
	if err != nil {
		t.Fatalf("ParseAgentToken: %v", err)
	}
	if claims.TenantID != tenantID {
		t.Errorf("TenantID = %v, want %v", claims.TenantID, tenantID)
	}
	if claims.SipUsername != "alice" || claims.SipDomain != "tring.local" {
		t.Errorf("sip claims = %s@%s, want alice@tring.local", claims.SipUsername, claims.SipDomain)
	}
	if claims.Subject != agentID.String() {
		t.Errorf("sub = %s, want %s", claims.Subject, agentID)
	}
}

func TestParseExpiredTokenRejected(t *testing.T) {
	s := newJWTTestServer("topsecret")
	// Negative TTL → expired at issue time.
	tok, _, _ := s.IssueAgentToken(uuid.New(), uuid.New(), "alice", "tring.local", -1*time.Minute)
	if _, err := s.ParseAgentToken(tok); err == nil {
		t.Error("ParseAgentToken accepted expired token")
	}
}

func TestParseWrongSecretRejected(t *testing.T) {
	issuer := newJWTTestServer("secret-A")
	verifier := newJWTTestServer("secret-B")
	tok, _, _ := issuer.IssueAgentToken(uuid.New(), uuid.New(), "alice", "tring.local", 5*time.Minute)

	if _, err := verifier.ParseAgentToken(tok); err == nil {
		t.Error("verifier with wrong secret accepted token")
	}
}

func TestParseGarbageRejected(t *testing.T) {
	s := newJWTTestServer("topsecret")
	for _, raw := range []string{"", "not.a.jwt", strings.Repeat("a.", 3)} {
		if _, err := s.ParseAgentToken(raw); err == nil {
			t.Errorf("ParseAgentToken(%q) accepted garbage", raw)
		}
	}
}

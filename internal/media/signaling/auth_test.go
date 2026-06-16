package signaling

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var authSecret = []byte("auth-test-secret")

func mintAgentTok(t *testing.T, tenant uuid.UUID, sip string) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant": tenant.String(), "sip_user": sip,
		"iss": jwtIssuer, "aud": agentAudience,
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(authSecret)
	if err != nil {
		t.Fatalf("sign agent: %v", err)
	}
	return s
}

func mintVisitorTok(t *testing.T, tenant uuid.UUID, flow, ref, jti string, exp time.Time) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant": tenant.String(), "flow_id": flow, "client_ref": ref,
		"iss": jwtIssuer, "aud": gatewayAudience, "jti": jti,
		"exp": exp.Unix(),
	}).SignedString(authSecret)
	if err != nil {
		t.Fatalf("sign visitor: %v", err)
	}
	return s
}

func TestValidatePrincipalAgent(t *testing.T) {
	tenant := uuid.New()
	p, err := validatePrincipal(mintAgentTok(t, tenant, "alice"), authSecret)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if p.isVisitor || p.sip != "alice" || p.tenant != tenant {
		t.Fatalf("agent principal = %+v", p)
	}
}

func TestValidatePrincipalVisitor(t *testing.T) {
	tenant := uuid.New()
	exp := time.Now().Add(2 * time.Minute)
	p, err := validatePrincipal(mintVisitorTok(t, tenant, "flow-1", "ref-9", "jti-1", exp), authSecret)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !p.isVisitor {
		t.Fatal("expected visitor principal")
	}
	if p.tenant != tenant || p.flowID != "flow-1" || p.clientRef != "ref-9" || p.jti != "jti-1" {
		t.Fatalf("visitor principal = %+v", p)
	}
	if p.exp.Unix() != exp.Unix() {
		t.Fatalf("exp = %v, want %v", p.exp.Unix(), exp.Unix())
	}
}

func TestValidatePrincipalRejectsWrongAudience(t *testing.T) {
	// A console-audience token is neither agent nor visitor → rejected.
	bad, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant": uuid.New().String(), "iss": jwtIssuer, "aud": "tring.console",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(authSecret)
	if _, err := validatePrincipal(bad, authSecret); err == nil {
		t.Fatal("expected rejection of non-agent/non-visitor audience")
	}
}

func TestValidatePrincipalRejectsBadSecret(t *testing.T) {
	tok := mintVisitorTok(t, uuid.New(), "f", "", "j", time.Now().Add(time.Minute))
	if _, err := validatePrincipal(tok, []byte("wrong-secret")); err == nil {
		t.Fatal("expected rejection of wrong-secret token")
	}
}

package orchestrator

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/whatomate/tring/internal/dbtest"
)

// TestExpireGuestSession: single-use credentials — once a guest call ends,
// the session row is deleted so the credential can't be replayed.
func TestExpireGuestSession(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	const sip = "guest_singleuse"
	_, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO guest_sessions (tenant_id, sip_username, sip_password, expires_at)
		VALUES ($1, $2, 'pw', now() + interval '15 min')
	`, f.TenantID, sip)
	if err != nil {
		t.Fatalf("seed guest_session: %v", err)
	}

	NewExecutor(f.DB).expireGuestSession(sip)

	var n int
	_ = f.DB.Pool.QueryRow(ctx,
		`SELECT count(*) FROM guest_sessions WHERE sip_username = $1`, sip).Scan(&n)
	if n != 0 {
		t.Errorf("guest session not expired: %d rows remain, want 0", n)
	}
}

func seedAgent(t *testing.T, f *dbtest.Fixture, sip string) {
	t.Helper()
	_, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO agents (tenant_id, sip_username, sip_password)
		VALUES ($1, $2, 'pw')
	`, f.TenantID, sip)
	if err != nil {
		t.Fatalf("insert agent %s: %v", sip, err)
	}
}

// TestAgentBridgeTargetRingsAll: with no agent pinned on the node, a Transfer
// rings ALL the tenant's agents simultaneously (comma-joined user/ targets).
func TestAgentBridgeTargetRingsAll(t *testing.T) {
	f := dbtest.Setup(t)
	seedAgent(t, f, "alice")
	seedAgent(t, f, "bob")
	e := NewExecutor(f.DB)

	node := &ivrNode{Type: nodeTransfer, Config: map[string]interface{}{}}
	got, err := e.agentBridgeTarget(context.Background(), node, f.TenantID)
	if err != nil {
		t.Fatalf("agentBridgeTarget: %v", err)
	}

	parts := strings.Split(got, ",") // comma = simultaneous ring
	sort.Strings(parts)
	want := []string{
		"user/alice@" + f.SIPDomain,
		"user/bob@" + f.SIPDomain,
	}
	if len(parts) != 2 || parts[0] != want[0] || parts[1] != want[1] {
		t.Errorf("bridge target = %q, want a comma-join of %v", got, want)
	}
}

// TestAgentBridgeTargetSpecificAgent: config.agent_sip pins the transfer to
// one agent.
func TestAgentBridgeTargetSpecificAgent(t *testing.T) {
	f := dbtest.Setup(t)
	seedAgent(t, f, "alice")
	seedAgent(t, f, "bob")
	e := NewExecutor(f.DB)

	node := &ivrNode{Type: nodeTransfer, Config: map[string]interface{}{"agent_sip": "bob"}}
	got, err := e.agentBridgeTarget(context.Background(), node, f.TenantID)
	if err != nil {
		t.Fatalf("agentBridgeTarget: %v", err)
	}
	if want := "user/bob@" + f.SIPDomain; got != want {
		t.Errorf("bridge target = %q, want %q", got, want)
	}
}

// TestAgentBridgeTargetNoAgents: a tenant with no agents yields "" so the
// Transfer node takes its no_answer branch instead of bridging to nothing.
func TestAgentBridgeTargetNoAgents(t *testing.T) {
	f := dbtest.Setup(t)
	e := NewExecutor(f.DB)

	got, err := e.agentBridgeTarget(context.Background(), &ivrNode{Config: map[string]interface{}{}}, f.TenantID)
	if err != nil {
		t.Fatalf("agentBridgeTarget: %v", err)
	}
	if got != "" {
		t.Errorf("bridge target = %q, want empty (no agents)", got)
	}
}

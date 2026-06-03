package orchestrator

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/whatomate/tring/internal/dbtest"
)

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
	got, sips, err := e.agentBridgeTarget(context.Background(), node, f.TenantID)
	if err != nil {
		t.Fatalf("agentBridgeTarget: %v", err)
	}

	parts := strings.Split(got, ",") // comma = simultaneous ring
	sort.Strings(parts)
	want := []string{
		"[tring_agent=alice]user/alice@" + f.SIPDomain,
		"[tring_agent=bob]user/bob@" + f.SIPDomain,
	}
	if len(parts) != 2 || parts[0] != want[0] || parts[1] != want[1] {
		t.Errorf("bridge target = %q, want a comma-join of %v", got, want)
	}
	// The candidate SIPs are returned so the answered agent can be attributed.
	sort.Strings(sips)
	if len(sips) != 2 || sips[0] != "alice" || sips[1] != "bob" {
		t.Errorf("candidate sips = %v, want [alice bob]", sips)
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
	got, sips, err := e.agentBridgeTarget(context.Background(), node, f.TenantID)
	if err != nil {
		t.Fatalf("agentBridgeTarget: %v", err)
	}
	if want := "[tring_agent=bob]user/bob@" + f.SIPDomain; got != want {
		t.Errorf("bridge target = %q, want %q", got, want)
	}
	if len(sips) != 1 || sips[0] != "bob" {
		t.Errorf("candidate sips = %v, want [bob]", sips)
	}
}

// TestAgentBridgeTargetNoAgents: a tenant with no agents yields "" so the
// Transfer node takes its no_answer branch instead of bridging to nothing.
func TestAgentBridgeTargetNoAgents(t *testing.T) {
	f := dbtest.Setup(t)
	e := NewExecutor(f.DB)

	got, sips, err := e.agentBridgeTarget(context.Background(), &ivrNode{Config: map[string]interface{}{}}, f.TenantID)
	if err != nil {
		t.Fatalf("agentBridgeTarget: %v", err)
	}
	if got != "" || len(sips) != 0 {
		t.Errorf("bridge target = %q, sips = %v, want empty (no agents)", got, sips)
	}
}

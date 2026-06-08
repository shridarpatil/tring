package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/dbtest"
)

// recordStep is pure (no DB / no ESL), so this asserts the journey shape that
// feeds calls.ivr_path and the console — including the "transfer A no-answer,
// then transfer B answered" sequence the call log needs to show.
func TestRecordStepBuildsJourney(t *testing.T) {
	st := &callState{}

	st.stepAction, st.stepDTMF = "prompt_dtmf", "1"
	st.recordStep(&ivrNode{ID: "m1", Type: "menu"}, "digit:1")

	st.stepAction, st.stepDetail, st.stepDTMF = "transfer", "alice", ""
	st.recordStep(&ivrNode{ID: "t1", Type: "transfer"}, "no_answer")

	st.stepAction, st.stepDetail = "transfer", "bob"
	st.recordStep(&ivrNode{ID: "t2", Type: "transfer"}, "completed")

	if len(st.steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(st.steps))
	}
	want := []SimStep{
		{NodeID: "m1", NodeType: "menu", Action: "prompt_dtmf", DTMFInput: "1", Outcome: "digit:1"},
		{NodeID: "t1", NodeType: "transfer", Action: "transfer", Detail: "alice", Outcome: "no_answer"},
		{NodeID: "t2", NodeType: "transfer", Action: "transfer", Detail: "bob", Outcome: "completed"},
	}
	for i, w := range want {
		if st.steps[i] != w {
			t.Errorf("step %d = %+v, want %+v", i, st.steps[i], w)
		}
	}
}

// recordInboundCall must insert a logged calls row (direction=inbound,
// status=ringing) so inbound/guest web calls show up in the call log —
// previously only the outbound path wrote a row.
func TestRecordInboundCallInsertsRow(t *testing.T) {
	f := dbtest.Setup(t)
	exec := NewExecutor(f.DB)
	ctx := context.Background()

	st := &callState{
		uuid:     "fs-uuid-inbound-1",
		tenantID: f.TenantID,
		caller:   "+919812345678",
		called:   "+918001112222",
		log:      slog.Default(),
	}
	exec.recordInboundCall(ctx, st)

	var direction, status, from, to string
	err := f.DB.Pool.QueryRow(ctx, `
		SELECT direction::text, status::text, from_number, to_number
		FROM calls WHERE fs_uuid = $1
	`, st.uuid).Scan(&direction, &status, &from, &to)
	if err != nil {
		t.Fatalf("expected a calls row for the inbound call: %v", err)
	}
	if direction != "inbound" || status != "ringing" {
		t.Errorf("direction/status = %s/%s, want inbound/ringing", direction, status)
	}
	if from != st.caller || to != st.called {
		t.Errorf("from/to = %s/%s, want %s/%s", from, to, st.caller, st.called)
	}
}

// saveIVRPath must persist the recorded journey to calls.ivr_path so the call
// detail can render it.
func TestSaveIVRPathPersists(t *testing.T) {
	f := dbtest.Setup(t)
	exec := NewExecutor(f.DB)
	ctx := context.Background()

	st := &callState{uuid: "fs-uuid-inbound-2", tenantID: f.TenantID, caller: "web", called: "support", log: slog.Default()}
	exec.recordInboundCall(ctx, st)

	st.steps = []SimStep{
		{NodeID: "m1", NodeType: "menu", Action: "prompt_dtmf", DTMFInput: "2", Outcome: "digit:2"},
		{NodeID: "t1", NodeType: "transfer", Action: "transfer", Detail: "alice", Outcome: "no_answer"},
		{NodeID: "t2", NodeType: "transfer", Action: "transfer", Detail: "bob", Outcome: "completed"},
	}
	exec.saveIVRPath(st)

	var raw []byte
	if err := f.DB.Pool.QueryRow(ctx,
		`SELECT ivr_path FROM calls WHERE fs_uuid = $1`, st.uuid,
	).Scan(&raw); err != nil {
		t.Fatalf("read ivr_path: %v", err)
	}
	var got []SimStep
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("ivr_path is not valid JSON: %v", err)
	}
	if len(got) != 3 || got[1].Outcome != "no_answer" || got[2].Outcome != "completed" {
		t.Errorf("persisted path = %+v, want the 3-step menu→A(no_answer)→B(completed) journey", got)
	}
}

// agentLeg tags each ringing leg with a tring_agent variable so the
// CHANNEL_BRIDGE handler can attribute the answer.
func TestAgentLegTagsSIP(t *testing.T) {
	e := &Executor{} // default "sip" transport
	if got := e.agentLeg("alice", "d.tring.local", "t1", ""); got != "[tring_agent=alice]user/alice@d.tring.local" {
		t.Errorf("agentLeg = %q", got)
	}
}

// With the "gateway" transport the leg routes to the media gateway as a SIP
// endpoint and carries the tenant id as a SIP header (ADR-001 M4b).
func TestAgentLegGatewayTransport(t *testing.T) {
	e := (&Executor{}).WithAgentTransport("gateway", "tring-gw")
	got := e.agentLeg("alice", "d.tring.local", "t1", "")
	want := "[tring_agent=alice,sip_h_X-Tring-Tenant=t1]sofia/gateway/tring-gw/alice"
	if got != want {
		t.Errorf("agentLeg = %q, want %q", got, want)
	}
}

// Once the bridge handler stamps calls.agent_id, the transfer step reads the
// answering agent back via answeredAgentLabel — the source of truth for "who
// answered" (and what the call-log Agent column shows).
func TestAnsweredAgentLabelAfterStamp(t *testing.T) {
	f := dbtest.Setup(t)
	seedAgent(t, f, "alice")
	exec := NewExecutor(f.DB)
	ctx := context.Background()

	st := &callState{uuid: "fs-uuid-answered", tenantID: f.TenantID, caller: "web", called: "support", log: slog.Default()}
	exec.recordInboundCall(ctx, st)

	// No agent yet → no label.
	if got := exec.answeredAgentLabel(st.uuid); got != "" {
		t.Errorf("answeredAgentLabel before stamp = %q, want empty", got)
	}

	// Simulate what onBridge does: resolve the agent and stamp the call.
	var agentID uuid.UUID
	var label string
	if err := f.DB.Pool.QueryRow(ctx, exec.Q.GetAgentBySIP, f.TenantID, "alice").Scan(&agentID, &label); err != nil {
		t.Fatalf("get-agent-by-sip: %v", err)
	}
	if _, err := f.DB.Pool.Exec(ctx, exec.Q.SetCallAgent, agentID, st.uuid, st.uuid+"-bleg"); err != nil {
		t.Fatalf("set-call-agent: %v", err)
	}

	if got := exec.answeredAgentLabel(st.uuid); got != "alice" { // no display_name → sip
		t.Errorf("answeredAgentLabel after stamp = %q, want alice", got)
	}
}

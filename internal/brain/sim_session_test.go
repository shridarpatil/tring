package brain

import (
	"testing"

	"github.com/google/uuid"
)

// Interactive sessions: walker pauses on every Menu/Gather, surfaces an
// awaiting_node, and resumes when Input is called. These cover the
// transitions a UI cares about — start → pause, input → next pause,
// input → end.

const interactiveMenuFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"g","type":"greeting","config":{"greeting_text":"Welcome"}},
    {"id":"m","type":"menu","config":{
      "greeting_text":"Press 1 for sales, 2 for support",
      "options":{"1":{"label":"Sales"},"2":{"label":"Support"}}
    }},
    {"id":"sales","type":"hangup","config":{}},
    {"id":"support","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"g","condition":"default"},
    {"from":"g","to":"m","condition":"default"},
    {"from":"m","to":"sales","condition":"digit:1"},
    {"from":"m","to":"support","condition":"digit:2"}
  ]
}`

func TestSessionStartPausesAtFirstMenu(t *testing.T) {
	m := NewSimManager()
	snap, err := m.Start(simOwner, []byte(interactiveMenuFlow), SimInput{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if snap.Status != StatusAwaitingDTMF {
		t.Errorf("status = %s, want awaiting_dtmf", snap.Status)
	}
	if snap.AwaitingNode == nil || snap.AwaitingNode.NodeID != "m" {
		t.Errorf("awaiting_node = %+v, want node id 'm'", snap.AwaitingNode)
	}
	// Steps so far: start passthrough + greeting. Menu hasn't run yet.
	if len(snap.Steps) != 2 {
		t.Errorf("steps = %d, want 2 (start + greeting before pause)", len(snap.Steps))
	}
}

func TestSessionInputAdvancesToTerminal(t *testing.T) {
	m := NewSimManager()
	snap, _ := m.Start(simOwner, []byte(interactiveMenuFlow), SimInput{})
	snap, err := m.Input(simOwner, snap.SessionID, "1")
	if err != nil {
		t.Fatalf("Input: %v", err)
	}
	if snap.Status != StatusEnded {
		t.Errorf("status = %s, want ended", snap.Status)
	}
	if snap.EndedAt != "sales" {
		t.Errorf("ended_at = %s, want sales (digit 1)", snap.EndedAt)
	}
}

func TestSessionUnknownIDReturns404Style(t *testing.T) {
	m := NewSimManager()
	_, err := m.Input(simOwner, "00000000-0000-0000-0000-000000000000", "1")
	if err == nil {
		t.Errorf("expected error for unknown session id; got nil")
	}
}

const interactiveTwoMenus = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"m1","type":"menu","config":{"options":{"1":{"label":"A"}}}},
    {"id":"m2","type":"menu","config":{"options":{"2":{"label":"B"}}}},
    {"id":"end","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"m1","condition":"default"},
    {"from":"m1","to":"m2","condition":"digit:1"},
    {"from":"m2","to":"end","condition":"digit:2"}
  ]
}`

func TestSessionPausesBetweenSuccessiveMenus(t *testing.T) {
	m := NewSimManager()
	snap, _ := m.Start(simOwner, []byte(interactiveTwoMenus), SimInput{})
	if snap.AwaitingNode == nil || snap.AwaitingNode.NodeID != "m1" {
		t.Fatalf("first pause should be m1, got %+v", snap.AwaitingNode)
	}
	snap, _ = m.Input(simOwner, snap.SessionID, "1")
	if snap.AwaitingNode == nil || snap.AwaitingNode.NodeID != "m2" {
		t.Fatalf("after m1 we should pause at m2, got status=%s awaiting=%+v",
			snap.Status, snap.AwaitingNode)
	}
	snap, _ = m.Input(simOwner, snap.SessionID, "2")
	if snap.Status != StatusEnded {
		t.Errorf("after m2 should end; got %s", snap.Status)
	}
}

// simOwner is the tenant the session tests run as.
var simOwner = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func TestSessionInputFromAnotherTenantIsNotFound(t *testing.T) {
	m := NewSimManager()
	snap, err := m.Start(simOwner, []byte(interactiveMenuFlow), SimInput{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	other := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	if _, err := m.Input(other, snap.SessionID, "1"); err == nil {
		t.Fatal("another tenant drove the session; want session not found")
	}
	// The owner's session is untouched and still waiting for input.
	got, err := m.Input(simOwner, snap.SessionID, "1")
	if err != nil || len(got.Steps) <= len(snap.Steps) {
		t.Fatalf("owner input after foreign attempt: err=%v steps %d→%d", err, len(snap.Steps), len(got.Steps))
	}
}

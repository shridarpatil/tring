package brain

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
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

// A gather looping back on itself: every input writes vars["x"] and pauses
// again, so overlapping inputs keep mutating the session.
const interactiveGatherLoop = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"g","type":"gather","config":{"store_as":"x"}}
  ],
  "edges": [
    {"from":"start","to":"g","condition":"default"},
    {"from":"g","to":"g","condition":"default"}
  ]
}`

// Overlapping inputs on one session (a double submit, a script) must not
// race: an unsynchronised map write during the response's JSON encoding is
// a fatal runtime error that takes down the whole process.
func TestSessionConcurrentInputsDoNotRace(t *testing.T) {
	m := NewSimManager()
	snap, err := m.Start(simOwner, []byte(interactiveGatherLoop), SimInput{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				got, err := m.Input(simOwner, snap.SessionID, strconv.Itoa(i*100+j+1))
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := json.Marshal(got); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Like a live menu, the interactive session re-prompts on an invalid digit
// (staying paused on the menu) until max_retries is used up.
func TestSessionMenuRepromptsOnInvalidDigit(t *testing.T) {
	m := NewSimManager()
	snap, _ := m.Start(simOwner, []byte(interactiveMenuFlow), SimInput{})
	snap, _ = m.Input(simOwner, snap.SessionID, "9")
	if snap.Status != StatusAwaitingDTMF || snap.AwaitingNode == nil || snap.AwaitingNode.NodeID != "m" {
		t.Fatalf("after an invalid digit: status %s, awaiting %+v; want re-prompted at the menu", snap.Status, snap.AwaitingNode)
	}
	snap, _ = m.Input(simOwner, snap.SessionID, "1")
	if snap.Status != StatusEnded || snap.EndedAt != "sales" {
		t.Fatalf("valid digit after a retry: status %s ended_at %q, want ended at sales", snap.Status, snap.EndedAt)
	}
}

// Invalid on every attempt (1 + max_retries) takes max_retries.
func TestSessionMenuMaxRetries(t *testing.T) {
	m := NewSimManager()
	snap, _ := m.Start(simOwner, []byte(interactiveMenuFlow), SimInput{})
	for i := 0; i < 4; i++ {
		snap, _ = m.Input(simOwner, snap.SessionID, "9")
	}
	last := snap.Steps[len(snap.Steps)-1]
	if last.NodeID != "m" || last.Outcome != "max_retries" {
		t.Fatalf("last step %+v, want the menu taking max_retries", last)
	}
}

// The re-prompt after an invalid key shows the menu's invalid-input message
// first, as a live call plays it.
func TestSessionMenuRepromptShowsInvalidMessage(t *testing.T) {
	flow := strings.Replace(interactiveMenuFlow, `"greeting_text":"Press 1 for sales, 2 for support",`,
		`"greeting_text":"Press 1 for sales, 2 for support","invalid_text":"Sorry, that's not an option.",`, 1)
	m := NewSimManager()
	snap, _ := m.Start(simOwner, []byte(flow), SimInput{})
	snap, _ = m.Input(simOwner, snap.SessionID, "9")
	if snap.AwaitingNode == nil || !strings.HasPrefix(snap.AwaitingNode.Prompt, "Sorry, that's not an option.") {
		t.Fatalf("re-prompt = %+v, want the invalid-input message first", snap.AwaitingNode)
	}
}

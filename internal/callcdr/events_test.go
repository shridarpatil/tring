package callcdr

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/events"
)

func TestRingingEvent(t *testing.T) {
	id := uuid.New()
	at := time.Unix(1700000000, 0)
	m := ringingEvent(id, "t/web-room", "+15551234567", "Support flow", "web", "inbound", at)
	if m["id"] != id || m["status"] != "ringing" || m["source"] != "web" || m["direction"] != "inbound" {
		t.Fatalf("bad ringing event: %+v", m)
	}
	if m["from_number"] != "+15551234567" || m["to_number"] != "Support flow" {
		t.Fatalf("bad numbers: %+v", m)
	}
}

func TestRingingEventOutboundDirection(t *testing.T) {
	id := uuid.New()
	at := time.Unix(1700000000, 0)
	m := ringingEvent(id, "t/pstn-call", "16001122", "+919900112233", "phone", "outbound", at)
	if m["direction"] != "outbound" || m["source"] != "phone" {
		t.Fatalf("bad outbound ringing event: %+v", m)
	}
}

// TestRingingEventCarriesRoom: the dashboard shows supervisor controls only for
// calls with a room, so a call added from this event needs room_id.
func TestRingingEventCarriesRoom(t *testing.T) {
	m := ringingEvent(uuid.New(), "t/pstn-call", "+919800000001", "Support", "phone", "inbound", time.Unix(1700000000, 0))
	if m["room_id"] != "t/pstn-call" {
		t.Fatalf("room_id = %v, want t/pstn-call", m["room_id"])
	}
	if _, ok := ringingEvent(uuid.New(), "", "a", "b", "phone", "inbound", time.Unix(1700000000, 0))["room_id"]; ok {
		t.Fatal("room_id set for a call with no room")
	}
}

func TestOpenPhase(t *testing.T) {
	// Inbound lands in the IVR, not "ringing" — that was the bug.
	if got := openPhase("inbound"); got != "ivr" {
		t.Fatalf("inbound phase = %q, want ivr", got)
	}
	// Outbound is genuinely dialing the far end.
	if got := openPhase("outbound"); got != "ringing" {
		t.Fatalf("outbound phase = %q, want ringing", got)
	}
}

func TestRingingEventCarriesPhase(t *testing.T) {
	m := ringingEvent(uuid.New(), "", "+15551234567", "Support flow", "web", "inbound", time.Unix(1700000000, 0))
	if m["phase"] != "ivr" {
		t.Fatalf("inbound ringing event phase = %v, want ivr", m["phase"])
	}
}

func TestRoutingEvent(t *testing.T) {
	cid := uuid.New()
	ring := routingEvent(cid, "ringing", "Sales")
	if ring["call_id"] != cid || ring["phase"] != "ringing" || ring["target"] != "Sales" {
		t.Fatalf("bad ringing routing event: %+v", ring)
	}
	revert := routingEvent(cid, "ivr", "")
	if revert["phase"] != "ivr" || revert["target"] != "" {
		t.Fatalf("bad revert routing event: %+v", revert)
	}
}

func TestAnsweredEvent(t *testing.T) {
	cid := uuid.New()
	aid := uuid.New()
	m := answeredEvent(cid, &aid, "Alice Support", time.Unix(1700000001, 0))
	if m["call_id"] != cid {
		t.Fatalf("bad call_id: %+v", m)
	}
	if m["agent_id"].(*uuid.UUID) == nil || *m["agent_id"].(*uuid.UUID) != aid {
		t.Fatalf("bad agent_id: %+v", m)
	}
	if m["agent_name"] != "Alice Support" {
		t.Fatalf("bad agent_name: %+v", m)
	}
}

func TestEndedEvent(t *testing.T) {
	cid := uuid.New()
	m := endedEvent(cid, 42, "caller_hangup", time.Unix(1700000050, 0))
	if m["call_id"] != cid || m["duration_seconds"] != 42 || m["hangup_cause"] != "caller_hangup" {
		t.Fatalf("bad ended event: %+v", m)
	}
}

func TestSuperviseStartDetail(t *testing.T) {
	m := superviseStartDetail("Nidhi", "monitor")
	if m["action"] != "start" || m["mode"] != "monitor" || m["supervisor"] != "Nidhi" {
		t.Fatalf("bad start detail: %+v", m)
	}
}

func TestSuperviseModeDetail(t *testing.T) {
	m := superviseModeDetail("Nidhi", "whisper")
	if m["action"] != "mode" || m["mode"] != "whisper" || m["supervisor"] != "Nidhi" {
		t.Fatalf("bad mode detail: %+v", m)
	}
}

func TestSuperviseEndDetail(t *testing.T) {
	m := superviseEndDetail("Nidhi")
	if m["action"] != "end" || m["supervisor"] != "Nidhi" {
		t.Fatalf("bad end detail: %+v", m)
	}
	if _, ok := m["mode"]; ok {
		t.Fatalf("end detail must not carry mode: %+v", m)
	}
}

// fakeSink records publishes (used by integration-style tests / future use).
type fakeSink struct{ types []string }

func (f *fakeSink) Publish(_ context.Context, _, typ string, _ any) { f.types = append(f.types, typ) }

var _ events.Sink = (*fakeSink)(nil)

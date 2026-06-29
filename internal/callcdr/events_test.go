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
	m := ringingEvent(id, "+15551234567", "Support flow", "web", at)
	if m["id"] != id || m["status"] != "ringing" || m["source"] != "web" {
		t.Fatalf("bad ringing event: %+v", m)
	}
	if m["from_number"] != "+15551234567" || m["to_number"] != "Support flow" {
		t.Fatalf("bad numbers: %+v", m)
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

// fakeSink records publishes (used by integration-style tests / future use).
type fakeSink struct{ types []string }

func (f *fakeSink) Publish(_ context.Context, _, typ string, _ any) { f.types = append(f.types, typ) }

var _ events.Sink = (*fakeSink)(nil)

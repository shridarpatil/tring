package callcdr_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/brain"
	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/events"
)

// The gateway hands *callcdr.Log to the brain's AgentRouter by type assertion.
var _ brain.QueueStints = (*callcdr.Log)(nil)

// stintSink records queue.stint payloads as decoded JSON.
type stintSink struct {
	mu  sync.Mutex
	got []map[string]any
}

func (s *stintSink) Publish(_ context.Context, _, typ string, data any) {
	if typ != events.TypeQueueStint {
		return
	}
	raw, _ := json.Marshal(data)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, m)
}

type stintRow struct {
	callID  *uuid.UUID
	team    string
	sla     int
	endedAt *time.Time
	outcome *string
}

func readStint(t *testing.T, fx *dbtest.Fixture, id uuid.UUID) stintRow {
	t.Helper()
	var r stintRow
	if err := fx.DB.Pool.QueryRow(context.Background(),
		`SELECT call_id, team, sla_seconds, ended_at, outcome FROM queue_stints WHERE id = $1 AND tenant_id = $2`,
		id, fx.TenantID).Scan(&r.callID, &r.team, &r.sla, &r.endedAt, &r.outcome); err != nil {
		t.Fatalf("read stint: %v", err)
	}
	return r
}

// Enter opens a stint tied to the room's call; leave closes it once.
func TestQueueEnterLeave(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	sink := &stintSink{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, sink)
	roomID := fx.TenantID.String() + "/pstn-stint"
	l.Started(ctx, fx.TenantID, roomID, "+919800000001", "Support", "phone", "", "caller-pid")
	callID := l.CallID(roomID)
	if callID == uuid.Nil {
		t.Fatal("call not started")
	}

	id := l.QueueEnter(ctx, roomID, "support", 300)
	if id == uuid.Nil {
		t.Fatal("QueueEnter returned uuid.Nil")
	}
	r := readStint(t, fx, id)
	if r.callID == nil || *r.callID != callID || r.team != "support" || r.sla != 300 || r.endedAt != nil {
		t.Fatalf("open stint = %+v", r)
	}

	l.QueueLeave(ctx, id, roomID, "support", "abandoned")
	r = readStint(t, fx, id)
	if r.endedAt == nil || r.outcome == nil || *r.outcome != "abandoned" {
		t.Fatalf("closed stint = %+v", r)
	}
	ended := *r.endedAt

	l.QueueLeave(ctx, id, roomID, "support", "answered")
	r = readStint(t, fx, id)
	if !r.endedAt.Equal(ended) || *r.outcome != "abandoned" {
		t.Fatalf("second leave changed the stint: %+v", r)
	}

	if len(sink.got) != 3 {
		t.Fatalf("events = %v, want 3", sink.got)
	}
	enter, leave := sink.got[0], sink.got[1]
	if enter["call_id"] != callID.String() || enter["team"] != "support" || enter["queued_at"] == nil || enter["sla_seconds"] != float64(300) {
		t.Fatalf("enter event = %v", enter)
	}
	if leave["call_id"] != callID.String() || leave["team"] != "support" || leave["queued_at"] != nil {
		t.Fatalf("leave event = %v", leave)
	}
}

// A caller who hangs up ends the call before the routing loop leaves the
// queue; the leave event still carries the call id from the enter.
func TestQueueLeaveAfterCallEnded(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	sink := &stintSink{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, sink)
	roomID := fx.TenantID.String() + "/pstn-hangup"
	l.Started(ctx, fx.TenantID, roomID, "+919800000001", "Support", "phone", "", "caller-pid")
	callID := l.CallID(roomID)

	id := l.QueueEnter(ctx, roomID, "support", 60)
	l.Ended(roomID, "caller_hangup")
	if l.CallID(roomID) != uuid.Nil {
		t.Fatal("call still live after Ended")
	}
	l.QueueLeave(ctx, id, roomID, "support", "abandoned")

	if len(sink.got) != 2 || sink.got[1]["call_id"] != callID.String() {
		t.Fatalf("events = %v, want leave with call_id %s", sink.got, callID)
	}
	if r := readStint(t, fx, id); r.outcome == nil || *r.outcome != "abandoned" {
		t.Fatalf("stint = %+v", r)
	}
}

// A room with no live call still records the stint, with call_id NULL.
func TestQueueEnterUnknownRoom(t *testing.T) {
	fx := dbtest.Setup(t)
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil)
	id := l.QueueEnter(context.Background(), fx.TenantID.String()+"/no-call", "support", 0)
	if id == uuid.Nil {
		t.Fatal("QueueEnter returned uuid.Nil")
	}
	if r := readStint(t, fx, id); r.callID != nil {
		t.Fatalf("call_id = %v, want NULL", *r.callID)
	}
}

// A nil *Log (gateway without a DB) is a no-op, never a panic.
func TestQueueStintsNilLog(t *testing.T) {
	var l *callcdr.Log
	if id := l.QueueEnter(context.Background(), uuid.NewString()+"/r", "support", 60); id != uuid.Nil {
		t.Fatalf("id = %v, want uuid.Nil", id)
	}
	l.QueueLeave(context.Background(), uuid.Nil, uuid.NewString()+"/r", "support", "abandoned")
}

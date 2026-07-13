package callcdr

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/events"
)

// captureSink records the (type, data) of every publish for assertions.
type captureSink struct {
	mu   sync.Mutex
	pubs []capturePub
}

type capturePub struct {
	typ  string
	data map[string]any
}

func (c *captureSink) Publish(_ context.Context, _, typ string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, _ := data.(map[string]any)
	c.pubs = append(c.pubs, capturePub{typ: typ, data: m})
}

var _ events.Sink = (*captureSink)(nil)

// newTestLog builds a Log with a capture sink and one active call registered,
// bypassing the DB (RingStart's team/ring-all path + RingStop don't touch it).
func newTestLog(roomID string, ac *activeCall) (*Log, *captureSink) {
	sink := &captureSink{}
	l := &Log{events: sink, calls: map[string]*activeCall{roomID: ac}}
	return l, sink
}

func TestRingStartPublishesTarget(t *testing.T) {
	ac := &activeCall{id: uuid.New(), tenant: uuid.New()}
	l, sink := newTestLog("t/room", ac)

	l.RingStart(context.Background(), "t/room", "Sales", "")

	if len(sink.pubs) != 1 || sink.pubs[0].typ != events.TypeCallRouting {
		t.Fatalf("want one call.routing publish, got %+v", sink.pubs)
	}
	if got := sink.pubs[0].data["target"]; got != "Sales" {
		t.Fatalf("target = %v, want Sales", got)
	}
	if got := sink.pubs[0].data["phase"]; got != "ringing" {
		t.Fatalf("phase = %v, want ringing", got)
	}
}

func TestRingStartRingAllTarget(t *testing.T) {
	ac := &activeCall{id: uuid.New(), tenant: uuid.New()}
	l, sink := newTestLog("t/room", ac)

	l.RingStart(context.Background(), "t/room", "", "") // no team, no agent → ring all

	if len(sink.pubs) != 1 || sink.pubs[0].data["target"] != "Available agents" {
		t.Fatalf("ring-all target wrong: %+v", sink.pubs)
	}
}

func TestRingStopRevertsToIVR(t *testing.T) {
	ac := &activeCall{id: uuid.New(), tenant: uuid.New()}
	l, sink := newTestLog("t/room", ac)

	l.RingStop("t/room")

	if len(sink.pubs) != 1 || sink.pubs[0].typ != events.TypeCallRouting {
		t.Fatalf("want one call.routing publish, got %+v", sink.pubs)
	}
	if got := sink.pubs[0].data["phase"]; got != "ivr" {
		t.Fatalf("phase = %v, want ivr", got)
	}
}

func TestRingStopNoOpAfterAnswered(t *testing.T) {
	ac := &activeCall{id: uuid.New(), tenant: uuid.New(), answered: true}
	l, sink := newTestLog("t/room", ac)

	l.RingStop("t/room") // must NOT undo the "talking" state

	if len(sink.pubs) != 0 {
		t.Fatalf("RingStop after answered should be a no-op, got %+v", sink.pubs)
	}
}

func TestRingStartUnknownRoomNoOp(t *testing.T) {
	l, sink := newTestLog("t/room", &activeCall{id: uuid.New(), tenant: uuid.New()})
	l.RingStart(context.Background(), "t/other", "Sales", "")
	l.RingStop("t/other")
	if len(sink.pubs) != 0 {
		t.Fatalf("unknown room should be a no-op, got %+v", sink.pubs)
	}
}

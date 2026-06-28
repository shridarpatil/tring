package events

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

// newTestHub builds a Hub with no Redis connection — enough to exercise
// Subscribe/dispatch/handle directly.
func newTestHub() *Hub {
	return &Hub{
		log:     slog.Default(),
		subs:    map[string]map[*Subscriber]struct{}{},
		forward: map[string]bool{},
		recomp:  map[string][]*recomputer{},
		pending: map[string]bool{},
		after:   func(_ time.Duration, _ func()) {}, // unused in this test file path
	}
}

func TestForwardDispatch(t *testing.T) {
	h := newTestHub()
	h.Forward(TypeCallEnded)
	sub := h.Subscribe("t1")

	h.handle("t1", Event{Type: TypeCallEnded, Data: json.RawMessage(`{"call_id":"c1"}`)})

	select {
	case frame := <-sub.Frames():
		want := "event: call.ended\ndata: {\"call_id\":\"c1\"}\n\n"
		if string(frame) != want {
			t.Fatalf("frame = %q", frame)
		}
	default:
		t.Fatal("expected a frame")
	}
}

func TestTenantIsolation(t *testing.T) {
	h := newTestHub()
	h.Forward(TypeCallEnded)
	a := h.Subscribe("tenant-a")
	b := h.Subscribe("tenant-b")

	h.handle("tenant-a", Event{Type: TypeCallEnded, Data: json.RawMessage(`{}`)})

	if len(a.Frames()) != 1 {
		t.Fatalf("tenant-a got %d frames, want 1", len(a.Frames()))
	}
	if len(b.Frames()) != 0 {
		t.Fatalf("tenant-b got %d frames, want 0", len(b.Frames()))
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	h := newTestHub()
	h.Forward(TypeCallEnded)
	sub := h.Subscribe("t1")
	h.Unsubscribe(sub)
	h.handle("t1", Event{Type: TypeCallEnded, Data: json.RawMessage(`{}`)})
	if _, ok := <-sub.Frames(); ok {
		t.Fatal("channel should be closed and drained after Unsubscribe")
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	h := newTestHub()
	h.Forward(TypeCallEnded)
	sub := h.Subscribe("t1")
	// Fill the buffer (subBuffer) + 1 to force a drop.
	for i := 0; i < subBuffer+1; i++ {
		h.handle("t1", Event{Type: TypeCallEnded, Data: json.RawMessage(`{}`)})
	}
	h.mu.Lock()
	_, present := h.subs["t1"][sub]
	h.mu.Unlock()
	if present {
		t.Fatal("slow subscriber should have been dropped")
	}
}

func TestNotForwardedTypeIgnored(t *testing.T) {
	h := newTestHub()
	// TypeQueueDepth is NOT forwarded (no Forward call).
	sub := h.Subscribe("t1")
	h.handle("t1", Event{Type: TypeQueueDepth, Data: json.RawMessage(`{}`)})
	if len(sub.Frames()) != 0 {
		t.Fatal("non-forwarded event should not reach subscribers")
	}
}

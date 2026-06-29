package events

import (
	"context"
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
		after:   func(_ time.Duration, _ func()) {},
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

func TestRecomputeCoalesces(t *testing.T) {
	h := newTestHub()
	var fired []func()
	h.after = func(_ time.Duration, f func()) { fired = append(fired, f) }

	builds := 0
	h.Recompute([]string{TypeCallEnded, TypeCallRinging}, TypeStats, time.Second,
		func(_ context.Context, tenant string) ([]byte, error) {
			builds++
			return []byte(`{"calls_today":3}`), nil
		})

	sub := h.Subscribe("t1")

	// Three triggers before the debounce fires → ONE scheduled recompute.
	h.handle("t1", Event{Type: TypeCallEnded, Data: json.RawMessage(`{}`)})
	h.handle("t1", Event{Type: TypeCallRinging, Data: json.RawMessage(`{}`)})
	h.handle("t1", Event{Type: TypeCallEnded, Data: json.RawMessage(`{}`)})

	if len(fired) != 1 {
		t.Fatalf("scheduled %d recomputes, want 1 (coalesced)", len(fired))
	}
	fired[0]() // simulate the debounce firing

	if builds != 1 {
		t.Fatalf("build called %d times, want 1", builds)
	}
	select {
	case frame := <-sub.Frames():
		want := "event: stats\ndata: {\"calls_today\":3}\n\n"
		if string(frame) != want {
			t.Fatalf("frame = %q", frame)
		}
	default:
		t.Fatal("expected a stats frame")
	}
}

func TestRecomputeRearmsAfterFire(t *testing.T) {
	h := newTestHub()
	var fired []func()
	h.after = func(_ time.Duration, f func()) { fired = append(fired, f) }
	h.Recompute([]string{TypeCallEnded}, TypeStats, time.Second,
		func(_ context.Context, _ string) ([]byte, error) { return []byte(`{}`), nil })
	h.Subscribe("t1")

	h.handle("t1", Event{Type: TypeCallEnded, Data: json.RawMessage(`{}`)})
	fired[0]() // first window fires and clears pending
	h.handle("t1", Event{Type: TypeCallEnded, Data: json.RawMessage(`{}`)})
	if len(fired) != 2 {
		t.Fatalf("scheduled %d, want 2 (re-armed after fire)", len(fired))
	}
}

package room_test

import (
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/media/room"
)

// ─── test doubles ────────────────────────────────────────────────────

// manualClock is a deterministic room.Clock: AfterFunc registers a callback but
// never fires on its own; the test fires pending timers with fireAll. Stop
// removes a timer from the pending set (and reports whether it was still there).
type manualClock struct {
	mu     sync.Mutex
	timers map[*manualTimer]struct{}
}

func newManualClock() *manualClock {
	return &manualClock{timers: map[*manualTimer]struct{}{}}
}

func (c *manualClock) AfterFunc(_ time.Duration, f func()) room.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{c: c, f: f}
	c.timers[t] = struct{}{}
	return t
}

// fireAll runs every still-pending timer callback (simulating the grace window
// elapsing). Stopped timers are already gone from the set, so they don't fire.
func (c *manualClock) fireAll() {
	c.mu.Lock()
	pending := make([]*manualTimer, 0, len(c.timers))
	for t := range c.timers {
		pending = append(pending, t)
	}
	c.timers = map[*manualTimer]struct{}{}
	c.mu.Unlock()
	for _, t := range pending {
		t.f()
	}
}

func (c *manualClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type manualTimer struct {
	c *manualClock
	f func()
}

func (t *manualTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	_, ok := t.c.timers[t]
	delete(t.c.timers, t)
	return ok
}

// recorder captures emitted events for assertions. emit is invoked without the
// room lock held, so its own mutex is sufficient.
type recorder struct {
	mu     sync.Mutex
	events []room.Event
}

func (r *recorder) emit(e room.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// lastState returns the latest StateParticipantState value seen for pid, and
// whether any was recorded.
func (r *recorder) lastState(pid room.ParticipantID) (room.State, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var st room.State
	found := false
	for _, e := range r.events {
		if e.Type == room.EventParticipantState && e.Participant == pid {
			st, found = e.State, true
		}
	}
	return st, found
}

func (r *recorder) count(t room.EventType, pid room.ParticipantID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Type == t && e.Participant == pid {
			n++
		}
	}
	return n
}

// newManager wires a manager with a manual clock + recorder for a test.
func newManager(t *testing.T, grace time.Duration) (*room.Manager, *manualClock, *recorder) {
	t.Helper()
	clk := newManualClock()
	rec := &recorder{}
	m := room.NewManager(room.Options{Grace: grace, Clock: clk, Emitter: rec.emit})
	return m, clk, rec
}

// ─── tests ───────────────────────────────────────────────────────────

func TestJoinCreatesSlotWithTokenAndEvents(t *testing.T) {
	m, _, rec := newManager(t, time.Minute)
	p, err := m.Join("room-1", "Visitor")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if p.ID() == "" || p.Token() == "" {
		t.Fatalf("expected id+token, got id=%q token=%q", p.ID(), p.Token())
	}
	if p.Name() != "Visitor" {
		t.Fatalf("name = %q, want Visitor", p.Name())
	}
	if got := p.State(); got != room.StateJoining {
		t.Fatalf("state = %v, want joining", got)
	}
	if n := rec.count(room.EventParticipantJoined, p.ID()); n != 1 {
		t.Fatalf("joined events = %d, want 1", n)
	}
	if st, _ := rec.lastState(p.ID()); st != room.StateJoining {
		t.Fatalf("last state event = %v, want joining", st)
	}
}

func TestMarkConnectedTransitions(t *testing.T) {
	m, _, rec := newManager(t, time.Minute)
	p, _ := m.Join("r", "A")
	p.MarkConnected()
	if got := p.State(); got != room.StateConnected {
		t.Fatalf("state = %v, want connected", got)
	}
	if st, _ := rec.lastState(p.ID()); st != room.StateConnected {
		t.Fatalf("last state event = %v, want connected", st)
	}
}

func TestNPartyRoomSharing(t *testing.T) {
	m, _, _ := newManager(t, time.Minute)
	a, _ := m.Join("call", "A")
	b, _ := m.Join("call", "B")
	c, _ := m.Join("call", "C")
	if m.Len() != 1 {
		t.Fatalf("rooms = %d, want 1 (shared)", m.Len())
	}
	r, ok := m.Get("call")
	if !ok {
		t.Fatal("room not found")
	}
	if got := len(r.Participants()); got != 3 {
		t.Fatalf("participants = %d, want 3", got)
	}
	// distinct identities + tokens
	ids := map[room.ParticipantID]bool{a.ID(): true, b.ID(): true, c.ID(): true}
	if len(ids) != 3 {
		t.Fatalf("expected 3 distinct ids, got %v", ids)
	}
	if a.Token() == b.Token() || b.Token() == c.Token() || a.Token() == c.Token() {
		t.Fatal("tokens must be distinct")
	}
}

func TestDropArmsGraceAndKeepsPeer(t *testing.T) {
	m, clk, rec := newManager(t, 30*time.Second)
	a, _ := m.Join("call", "A")
	b, _ := m.Join("call", "B")
	a.MarkConnected()
	b.MarkConnected()

	a.Drop()
	if got := a.State(); got != room.StateReconnecting {
		t.Fatalf("A state = %v, want reconnecting", got)
	}
	if clk.pending() != 1 {
		t.Fatalf("grace timers pending = %d, want 1", clk.pending())
	}
	if st, _ := rec.lastState(a.ID()); st != room.StateReconnecting {
		t.Fatalf("A last state = %v, want reconnecting", st)
	}
	// Peer B is untouched and still in the room.
	if got := b.State(); got != room.StateConnected {
		t.Fatalf("B state = %v, want connected (peer must not drop)", got)
	}
	r, _ := m.Get("call")
	if len(r.Participants()) != 2 {
		t.Fatalf("participants = %d, want 2 (slot held during grace)", len(r.Participants()))
	}
}

func TestReattachWithinGraceRecovers(t *testing.T) {
	m, clk, rec := newManager(t, 30*time.Second)
	a, _ := m.Join("call", "A")
	a.MarkConnected()
	a.Drop()

	got, err := m.Reattach("call", a.Token())
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if got.ID() != a.ID() {
		t.Fatalf("reattached to %q, want same slot %q", got.ID(), a.ID())
	}
	// Reattach cancels the eviction timer...
	if clk.pending() != 0 {
		t.Fatalf("grace timers pending after reattach = %d, want 0", clk.pending())
	}
	// ...so even if the window "elapses" now, the slot is NOT evicted.
	clk.fireAll()
	if st := a.State(); st == room.StateLeft {
		t.Fatal("slot was evicted after reattach; grace timer should have been cancelled")
	}
	// Fresh PeerConnection comes up → connected.
	a.MarkConnected()
	if st := a.State(); st != room.StateConnected {
		t.Fatalf("state = %v, want connected after reattach", st)
	}
	if st, _ := rec.lastState(a.ID()); st != room.StateConnected {
		t.Fatalf("last state = %v, want connected", st)
	}
}

func TestGraceExpiryEvictsSlot(t *testing.T) {
	m, clk, rec := newManager(t, 30*time.Second)
	a, _ := m.Join("call", "A")
	b, _ := m.Join("call", "B")
	a.MarkConnected()
	b.MarkConnected()
	token := a.Token()

	a.Drop()
	clk.fireAll() // grace window elapses without a reattach

	if st := a.State(); st != room.StateLeft {
		t.Fatalf("A state = %v, want left after grace expiry", st)
	}
	if n := rec.count(room.EventParticipantLeft, a.ID()); n != 1 {
		t.Fatalf("left events = %d, want 1", n)
	}
	// Slot is gone; a stale token no longer reattaches.
	if _, err := m.Reattach("call", token); err != room.ErrUnknownSession {
		t.Fatalf("reattach stale token err = %v, want ErrUnknownSession", err)
	}
	// Peer B survived the whole time.
	if st := b.State(); st != room.StateConnected {
		t.Fatalf("B state = %v, want connected", st)
	}
	r, _ := m.Get("call")
	if len(r.Participants()) != 1 {
		t.Fatalf("participants = %d, want 1 (A evicted)", len(r.Participants()))
	}
}

func TestLeaveRemovesAndClosesEmptyRoom(t *testing.T) {
	m, _, rec := newManager(t, time.Minute)
	a, _ := m.Join("call", "A")
	b, _ := m.Join("call", "B")

	a.Leave()
	if st := a.State(); st != room.StateLeft {
		t.Fatalf("A state = %v, want left", st)
	}
	if rec.count(room.EventParticipantLeft, a.ID()) != 1 {
		t.Fatal("want one left event for A")
	}
	if m.Len() != 1 {
		t.Fatalf("rooms = %d, want 1 (B still present)", m.Len())
	}
	b.Leave()
	if m.Len() != 0 {
		t.Fatalf("rooms = %d, want 0 (room dropped when empty)", m.Len())
	}
}

func TestDropWhileJoiningThenEvict(t *testing.T) {
	m, clk, _ := newManager(t, 30*time.Second)
	a, _ := m.Join("call", "A") // never MarkConnected → still joining
	a.Drop()
	if st := a.State(); st != room.StateReconnecting {
		t.Fatalf("state = %v, want reconnecting", st)
	}
	clk.fireAll()
	if st := a.State(); st != room.StateLeft {
		t.Fatalf("state = %v, want left", st)
	}
}

func TestReattachUnknownRoomOrToken(t *testing.T) {
	m, _, _ := newManager(t, time.Minute)
	if _, err := m.Reattach("nope", "whatever"); err != room.ErrUnknownSession {
		t.Fatalf("unknown room err = %v, want ErrUnknownSession", err)
	}
	m.Join("call", "A")
	if _, err := m.Reattach("call", "bad-token"); err != room.ErrUnknownSession {
		t.Fatalf("unknown token err = %v, want ErrUnknownSession", err)
	}
}

func TestIdempotentTransitions(t *testing.T) {
	m, clk, rec := newManager(t, 30*time.Second)
	a, _ := m.Join("call", "A")
	a.MarkConnected()

	a.Drop()
	a.Drop() // second drop is a no-op: no extra timer, no extra event
	if clk.pending() != 1 {
		t.Fatalf("timers pending = %d, want 1 (double Drop must not re-arm)", clk.pending())
	}
	if n := rec.count(room.EventParticipantState, a.ID()); n != 3 {
		// joining, connected, reconnecting — the 2nd Drop adds nothing.
		t.Fatalf("state events = %d, want 3", n)
	}

	a.Leave()
	a.Leave()         // no-op
	a.MarkConnected() // no-op after left
	a.Drop()          // no-op after left
	if st := a.State(); st != room.StateLeft {
		t.Fatalf("state = %v, want left (terminal)", st)
	}
	if n := rec.count(room.EventParticipantLeft, a.ID()); n != 1 {
		t.Fatalf("left events = %d, want exactly 1", n)
	}
}

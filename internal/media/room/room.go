package room

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultGrace is the reconnecting-window default: how long a dropped
// participant's slot (and the peer) is held awaiting a reattach. The source
// design suggests ~30s.
const DefaultGrace = 30 * time.Second

// ErrUnknownSession is returned by Reattach when the token matches no live slot
// — it was never issued, or its grace window already expired and the slot was
// evicted. The caller should treat the client as a fresh joiner.
var ErrUnknownSession = errors.New("room: unknown or expired session token")

// ErrRoomClosed is returned when joining a room that has been torn down.
var ErrRoomClosed = errors.New("room: closed")

// Room is one call: a set of participants keyed by id, plus a token index for
// reattach. All mutation goes through the methods below, which mutate under mu
// and emit events AFTER unlocking so an emitter can safely re-enter the room.
type Room struct {
	id    RoomID
	grace time.Duration
	clock Clock
	emit  Emitter

	mu      sync.Mutex
	parts   map[ParticipantID]*Participant
	byToken map[string]*Participant
	closed  bool

	// onEmpty runs (once, after unlock) when the last participant leaves, so the
	// manager can drop the room. nil for a standalone room.
	onEmpty func()
}

// ID returns the room's id.
func (r *Room) ID() RoomID { return r.id }

// Participants returns a snapshot of the room's non-left participants — the
// signaling layer uses it to tell a new joiner who's already here.
func (r *Room) Participants() []*Participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Participant, 0, len(r.parts))
	for _, p := range r.parts {
		if p.state != StateLeft {
			out = append(out, p)
		}
	}
	return out
}

// Join adds a fresh participant in the joining state and returns it. The
// returned participant carries the opaque session token the client must store
// and re-present to Reattach after a refresh.
func (r *Room) Join(name string) (*Participant, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrRoomClosed
	}
	p := &Participant{
		id:    ParticipantID(uuid.NewString()),
		name:  name,
		token: uuid.NewString(),
		room:  r,
		state: StateJoining,
	}
	r.parts[p.id] = p
	r.byToken[p.token] = p
	r.mu.Unlock()

	r.emitOne(Event{Type: EventParticipantJoined, Room: r.id, Participant: p.id, Name: name})
	r.emitOne(Event{Type: EventParticipantState, Room: r.id, Participant: p.id, State: StateJoining})
	return p, nil
}

// Reattach resumes the slot identified by token after the client reconnected
// (e.g. a page refresh). It cancels the pending eviction and leaves the
// participant in reconnecting until the fresh PeerConnection calls
// MarkConnected. Returns ErrUnknownSession if the slot is gone.
func (r *Room) Reattach(token string) (*Participant, error) {
	r.mu.Lock()
	p := r.byToken[token]
	if p == nil || p.state == StateLeft {
		r.mu.Unlock()
		return nil, ErrUnknownSession
	}
	r.stopGrace(p)
	changed := p.state != StateReconnecting
	p.state = StateReconnecting
	r.mu.Unlock()

	// Surface reconnecting only if it wasn't already (a reattach before the WS
	// close was even processed shouldn't spam the peer). The transition to
	// connected comes later via MarkConnected.
	if changed {
		r.emitOne(Event{Type: EventParticipantState, Room: r.id, Participant: p.id, State: StateReconnecting})
	}
	return p, nil
}

// markConnected moves a joining/reconnecting participant to connected and
// cancels any grace timer. Backs Participant.MarkConnected.
func (r *Room) markConnected(p *Participant) {
	r.mu.Lock()
	if p.state == StateLeft || p.state == StateConnected {
		r.mu.Unlock()
		return
	}
	r.stopGrace(p)
	p.state = StateConnected
	r.mu.Unlock()
	r.emitOne(Event{Type: EventParticipantState, Room: r.id, Participant: p.id, State: StateConnected})
}

// drop moves a connected/joining participant to reconnecting and arms the grace
// eviction timer. Backs Participant.Drop. No-op if already reconnecting/left.
func (r *Room) drop(p *Participant) {
	r.mu.Lock()
	if p.state == StateLeft || p.state == StateReconnecting {
		r.mu.Unlock()
		return
	}
	p.state = StateReconnecting
	r.stopGrace(p) // defensive; should be nil here
	// On expiry: evict the slot. The timer fires on its own goroutine (real
	// clock) and re-enters the room — safe, it takes mu itself.
	p.grace = r.clock.AfterFunc(r.grace, func() { r.evict(p) })
	r.mu.Unlock()
	r.emitOne(Event{Type: EventParticipantState, Room: r.id, Participant: p.id, State: StateReconnecting})
}

// leave removes a participant on an explicit bye. Terminal. Backs
// Participant.Leave.
func (r *Room) leave(p *Participant) {
	r.removeTerminal(p)
}

// evict is the grace-timer callback: the reconnecting window expired without a
// reattach, so the slot is gone. It only fires if the participant is still
// reconnecting (markConnected/leave stop the timer first).
func (r *Room) evict(p *Participant) {
	r.mu.Lock()
	if p.state != StateReconnecting {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.removeTerminal(p)
}

// removeTerminal transitions a participant to left, removes it from the room's
// indexes, and (after unlocking) emits left + fires onEmpty if the room is now
// empty. Idempotent: a second call after the slot is gone is a no-op.
func (r *Room) removeTerminal(p *Participant) {
	r.mu.Lock()
	if p.state == StateLeft {
		r.mu.Unlock()
		return
	}
	r.stopGrace(p)
	p.state = StateLeft
	delete(r.parts, p.id)
	delete(r.byToken, p.token)
	empty := len(r.parts) == 0
	r.mu.Unlock()

	r.emitOne(Event{Type: EventParticipantState, Room: r.id, Participant: p.id, State: StateLeft})
	r.emitOne(Event{Type: EventParticipantLeft, Room: r.id, Participant: p.id})
	if empty && r.onEmpty != nil {
		r.onEmpty()
	}
}

// stopGrace cancels and clears a participant's grace timer. Caller holds mu.
func (r *Room) stopGrace(p *Participant) {
	if p.grace != nil {
		p.grace.Stop()
		p.grace = nil
	}
}

// emitOne delivers one event to the configured emitter (no-op if none). Always
// called WITHOUT mu held.
func (r *Room) emitOne(e Event) {
	if r.emit != nil {
		r.emit(e)
	}
}

// Manager owns the live rooms and resolves a (room, token) to its slot for
// reattach. One Manager per gateway process; the signaling layer drives it.
type Manager struct {
	grace time.Duration
	clock Clock
	emit  Emitter

	mu    sync.Mutex
	rooms map[RoomID]*Room
}

// Options configures a Manager. Zero values are filled with sane defaults
// (DefaultGrace, RealClock, no-op emitter).
type Options struct {
	Grace   time.Duration
	Clock   Clock
	Emitter Emitter
}

// NewManager builds a Manager. deps hang off the returned struct (no package
// globals), matching the repo's style.
func NewManager(opts Options) *Manager {
	if opts.Grace <= 0 {
		opts.Grace = DefaultGrace
	}
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	return &Manager{
		grace: opts.Grace,
		clock: opts.Clock,
		emit:  opts.Emitter,
		rooms: make(map[RoomID]*Room),
	}
}

// Join adds a participant to room id, creating the room if needed.
func (m *Manager) Join(id RoomID, name string) (*Participant, error) {
	return m.room(id).Join(name)
}

// Reattach resumes a participant in an existing room. Returns ErrUnknownSession
// if the room or the token is gone (caller falls back to a fresh Join).
func (m *Manager) Reattach(id RoomID, token string) (*Participant, error) {
	m.mu.Lock()
	r := m.rooms[id]
	m.mu.Unlock()
	if r == nil {
		return nil, ErrUnknownSession
	}
	return r.Reattach(token)
}

// Get returns a live room by id.
func (m *Manager) Get(id RoomID) (*Room, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	return r, ok
}

// Len reports the number of live rooms (test/observability helper).
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rooms)
}

// room returns the room for id, creating it on first use. The room's onEmpty
// removes it from the manager when its last participant leaves.
func (m *Manager) room(id RoomID) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rooms[id]; ok {
		return r
	}
	r := &Room{
		id:      id,
		grace:   m.grace,
		clock:   m.clock,
		emit:    m.emit,
		parts:   make(map[ParticipantID]*Participant),
		byToken: make(map[string]*Participant),
	}
	r.onEmpty = func() { m.drop(id) }
	m.rooms[id] = r
	return r
}

// drop removes an empty room. Guards against racing a fresh Join into the same
// id: only removes the room still mapped under id (a new room would be a
// different pointer, but we also re-check emptiness).
func (m *Manager) drop(id RoomID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok {
		return
	}
	r.mu.Lock()
	empty := len(r.parts) == 0
	if empty {
		r.closed = true
	}
	r.mu.Unlock()
	if empty {
		delete(m.rooms, id)
	}
}

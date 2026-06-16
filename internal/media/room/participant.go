package room

// Participant is one slot in a room. Its identity (ID, Name) and session token
// are immutable for its lifetime; its State and grace timer are mutated only
// under the owning room's lock. The transport (PeerConnection) is NOT held here
// in M1 — the slot outlives any single transport, which is what makes refresh
// recovery possible.
type Participant struct {
	id    ParticipantID
	name  string
	token string // opaque session token; the client re-presents it to reattach
	room  *Room

	// state and grace are guarded by room.mu. grace is the eviction timer armed
	// while reconnecting; nil otherwise.
	state State
	grace Timer
}

// ID returns the participant's stable id.
func (p *Participant) ID() ParticipantID { return p.id }

// Name returns the participant's display label.
func (p *Participant) Name() string { return p.name }

// Token returns the opaque session token the client stores (sessionStorage) and
// re-presents on reconnect to reattach to this same slot. Treat as a secret —
// it authorizes resuming the participant.
func (p *Participant) Token() string { return p.token }

// Room returns the participant's room.
func (p *Participant) Room() *Room { return p.room }

// State returns the participant's current lifecycle state (lock-safe).
func (p *Participant) State() State {
	p.room.mu.Lock()
	defer p.room.mu.Unlock()
	return p.state
}

// MarkConnected transitions the participant to connected once its transport is
// up (initial negotiation completed, or a fresh PeerConnection after a
// reattach). Cancels any pending grace eviction. No-op if already left.
func (p *Participant) MarkConnected() { p.room.markConnected(p) }

// Drop is called when the participant's transport (signaling socket /
// PeerConnection) is lost. A connected/joining participant enters the grace
// window (reconnecting) and the eviction timer arms; the peer is notified but
// kept. Detect this from the WS close, not an ICE-failed timeout, so the grace
// timer starts immediately.
func (p *Participant) Drop() { p.room.drop(p) }

// Leave removes the participant explicitly (an intentional bye). Terminal.
func (p *Participant) Leave() { p.room.leave(p) }

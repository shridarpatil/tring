package signaling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/whatomate/tring/internal/media/room"
)

// errTokenReused is returned when a visitor token's jti was already consumed.
var errTokenReused = errors.New("visitor token already used")

// consumeVisitorToken burns a visitor token's jti single-use via the token store
// (Redis), mirroring the gateway. Fails CLOSED: no jti, expired, no store, or a
// store error all reject — a captured token can't be amplified into many calls.
func (s *Server) consumeVisitorToken(ctx context.Context, jti string, exp time.Time) error {
	if jti == "" {
		return errors.New("visitor token has no jti")
	}
	ttl := time.Until(exp)
	if ttl <= 0 {
		return errors.New("visitor token expired")
	}
	if s.cfg.Tokens == nil {
		return errors.New("single-use store unavailable")
	}
	fresh, err := s.cfg.Tokens.SetNX(ctx, "rtctoken:"+jti, []byte("1"), ttl)
	if err != nil {
		return fmt.Errorf("single-use check failed: %w", err)
	}
	if !fresh {
		return errTokenReused
	}
	return nil
}

// waitForConnected blocks (up to d) until a participant in the room has connected
// media, so the FIRST IVR prompt isn't played into a not-yet-connected path and
// missed (and the menu's input window doesn't start counting before the caller
// can hear it). Best-effort: returns after the timeout regardless.
func (s *Server) waitForConnected(roomID room.RoomID, d time.Duration) {
	if s.sfu.HasPort(roomID) {
		return // a phone/WhatsApp leg is media-ready the moment it's attached
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r, ok := s.rooms.Get(roomID); ok {
			for _, p := range r.Participants() {
				if p.State() == room.StateConnected {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ivrConnectWait bounds how long the first prompt waits for the caller's media.
const ivrConnectWait = 6 * time.Second

// removeParticipant drops a leg from the room (e.g. an agent who transferred the
// call out): it leaves the room (peers get peer-left) and its socket is closed,
// so the client sees the call end.
func (s *Server) removeParticipant(roomID room.RoomID, pid room.ParticipantID) {
	if r, ok := s.rooms.Get(roomID); ok {
		for _, p := range r.Participants() {
			if p.ID() == pid {
				p.Leave()
				break
			}
		}
	}
	s.mu.Lock()
	c := s.conns[pid]
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// endIfAlone ends the call when only the caller is left (no agent answered) so
// they aren't stranded in an empty room. It sends a terminal `ended` first so the
// client doesn't try to reconnect, then removes the participant.
func (s *Server) endIfAlone(roomID room.RoomID) {
	if s.sfu.HasPort(roomID) {
		// A phone/WhatsApp room: the "caller" is the port, so a single web
		// participant here is the AGENT serving them — never end that. The port
		// path has its own abandonment logic (hangupPstnIfAbandoned).
		return
	}
	r, ok := s.rooms.Get(roomID)
	if !ok {
		return
	}
	ps := r.Participants()
	if len(ps) != 1 { // 0 = already empty; ≥2 = an agent is present, keep the call
		return
	}
	pid := ps[0].ID()
	s.mu.Lock()
	c := s.conns[pid]
	s.mu.Unlock()
	if c != nil {
		_ = c.send(msg{Type: msgEnded, Reason: "flow ended"})
	}
	s.removeParticipant(roomID, pid)
}

package signaling

// clicktocall.go — backend click-to-call to a web agent (the API's POST
// /v1/calls): ring the agent's widget with an auto-answer `incoming` and, once
// they join the room, dial the customer over the trunk into that same room. The
// agent hears the tenant's hold music while the customer rings. The call is
// answered when the customer picks up — not when the agent joins — and from
// there it's an ordinary outbound call: one CDR, recorded, supervisable, ended
// by either side (outbound.go's teardown).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dial"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/room"
)

// callAgentTimeout bounds how long the agent's widget has to auto-answer. A var
// so tests can shorten it.
var callAgentTimeout = 30 * time.Second

var (
	errAgentOffline = errors.New("agent is not connected")
	errAgentBusy    = errors.New("agent is already on a call")
)

// CallRequest is one click-to-call: the agent (by SIP username) and the
// customer's number, plus the label the agent sees and an optional caller-ID
// override (must be one of the tenant's numbers).
type CallRequest struct {
	AgentSIP string
	To       string
	Label    string
	CallerID string
}

// Call validates the request, opens the call row (ringing) and places the call
// in the background, returning the call's id. Errors are returned only for
// calls never placed; an agent or customer who doesn't answer fails the row.
func (s *Server) Call(ctx context.Context, tenant uuid.UUID, req CallRequest) (uuid.UUID, error) {
	if s.cfg.Originator == nil || s.cfg.Trunks == nil || s.cfg.Calls == nil {
		return uuid.Nil, errDialUnavailable
	}
	to, err := dial.India(req.To)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", errDialInvalidNumber, err)
	}
	trunks, err := s.cfg.Trunks.OutboundTrunks(ctx, tenant)
	if err != nil || len(trunks) == 0 {
		return uuid.Nil, fmt.Errorf("%w: %v", errDialNoTrunk, err)
	}
	from := trunks[0].E164
	if req.CallerID != "" {
		owned, err := s.cfg.Trunks.OwnsNumber(ctx, tenant, req.CallerID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check caller id: %w", err) // a 500, not the caller's fault
		}
		if !owned {
			return uuid.Nil, fmt.Errorf("%w: %s", errDialCallerID, req.CallerID)
		}
		from = req.CallerID
	}
	agentKey := tenant.String() + "|" + req.AgentSIP
	s.mu.Lock()
	online := s.agents[tenant.String()][req.AgentSIP] != nil
	busy := s.onCall[agentKey] > 0
	s.mu.Unlock()
	if !online {
		return uuid.Nil, errAgentOffline
	}
	if busy {
		return uuid.Nil, errAgentBusy
	}

	name := "call-" + uuid.NewString()
	roomID := room.RoomID(tenant.String() + "/" + name)
	// Reserve the room's outbound slot now: the agent can't msgDial over it, and
	// their join below isn't taken as the answer (outboundPending).
	release, ok := s.beginDial(roomID, agentKey)
	if !ok {
		return uuid.Nil, errAgentBusy
	}
	if !s.startPortCall() {
		release()
		return uuid.Nil, errDraining
	}
	s.cfg.Calls.StartedOutbound(ctx, tenant, string(roomID), from, to, "", "")
	callID := s.cfg.Calls.CallID(string(roomID))
	if callID == uuid.Nil {
		s.endPortCall()
		release()
		return uuid.Nil, errConnectNoCDR
	}
	go s.runCall(roomID, name, tenant, req, to, release)
	return callID, nil
}

// runCall places a Call: the agent's widget first, then the customer, then
// waits for either side to hang up.
func (s *Server) runCall(roomID room.RoomID, name string, tenant uuid.UUID, req CallRequest, to string, release func()) {
	defer s.endPortCall()
	defer release()

	agentPID, ok := s.ringCallAgent(roomID, name, tenant.String(), req.AgentSIP, req.Label)
	if !ok {
		s.log.Warn("call: agent didn't answer", "room", roomID, "sip", req.AgentSIP)
		s.cfg.Calls.Failed(string(roomID), "agent_not_answered")
		s.endCallRoom(roomID) // a join that raced the timeout
		return
	}
	s.log.Info("call: agent joined, ringing customer", "room", roomID, "sip", req.AgentSIP)

	// The agent hears hold music while the customer rings, and hanging up stops
	// the ring.
	stopMusic := s.playHold(roomID, s.tenantHoldMusic(roomID))
	octx, cancel := context.WithTimeout(context.Background(), connectRingTimeout)
	go s.cancelWhenEmpty(octx, roomID, cancel)
	leg, _, _, err := s.originateOut(octx, tenant, to, req.CallerID, func(room.Progress) {
		if c := s.connFor(agentPID); c != nil {
			_ = c.send(msg{Type: msgRinging})
		}
	})
	cancel()
	stopMusic()
	if err != nil {
		// If the agent hung up first, their leave already settled the call
		// (agent_hangup), and this is a no-op.
		s.log.Warn("call: customer not connected", "room", roomID, "err", err)
		s.cfg.Calls.Failed(string(roomID), "client_not_answered")
		s.endCallRoom(roomID)
		return
	}
	clientPID := leg.Source.ID()
	if r, ok := s.rooms.Get(roomID); !ok || len(r.Participants()) == 0 {
		s.log.Warn("call: agent left as the customer answered, hanging up", "room", roomID)
		_ = s.cfg.Originator.HangupOutbound(clientPID)
		return
	}
	tagParticipant(clientPID, "callee")
	if err := s.sfu.AttachPort(roomID, leg.Source, leg.Write); err != nil {
		logging.ClearParticipant(string(clientPID))
		s.log.Error("call: attach customer failed", "room", roomID, "err", err)
		_ = s.cfg.Originator.HangupOutbound(clientPID)
		s.cfg.Calls.Failed(string(roomID), "system")
		s.endCallRoom(roomID)
		return
	}
	s.mu.Lock()
	s.outboundLegs[roomID] = clientPID // the agent leaving now hangs the customer up
	s.mu.Unlock()

	// Both connected: the call is answered and recording starts.
	if sink := s.cfg.Calls.Customer(string(roomID), clientPID); sink != nil {
		s.sfu.AddSink(roomID, clientPID, sink)
	}
	if sink := s.cfg.Calls.Answered(string(roomID), agentPID, req.AgentSIP); sink != nil {
		s.sfu.AddSink(roomID, agentPID, sink)
	}
	s.log.Info("call: connected", "room", roomID)

	<-leg.Done
	s.endOutboundRoom(roomID, "client_hangup") // /v1/calls/connect's name for it
}

// ringCallAgent rings sip's widget with an auto-answer incoming for the room and
// waits for them to join it, returning their participant id.
func (s *Server) ringCallAgent(roomID room.RoomID, name, tenant, sip, label string) (room.ParticipantID, bool) {
	ring := make(chan struct{})
	s.mu.Lock()
	c := s.agents[tenant][sip]
	s.rings[roomID] = ring // closed by signalRing when the agent joins
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.rings[roomID] == ring {
			delete(s.rings, roomID)
		}
		s.mu.Unlock()
	}()
	if c == nil || c.send(msg{Type: msgIncoming, Room: name, From: label, Auto: true}) != nil {
		return "", false
	}
	select {
	case <-ring:
	case <-time.After(callAgentTimeout):
		_ = c.send(msg{Type: msgCancelRing, Room: name})
		return "", false
	}
	r, ok := s.rooms.Get(roomID)
	if !ok || len(r.Participants()) == 0 {
		return "", false
	}
	return r.Participants()[0].ID(), true
}

// cancelWhenEmpty cancels the customer's ring if the agent leaves the room.
func (s *Server) cancelWhenEmpty(ctx context.Context, roomID room.RoomID, cancel func()) {
	tk := time.NewTicker(250 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if r, ok := s.rooms.Get(roomID); !ok || len(r.Participants()) == 0 {
				cancel()
				return
			}
		}
	}
}

// endCallRoom ends a Call that never connected: the agent still in the room
// gets a terminal `ended` and is removed.
func (s *Server) endCallRoom(roomID room.RoomID) {
	r, ok := s.rooms.Get(roomID)
	if !ok {
		return
	}
	s.setHangupBy(roomID, "system")
	for _, p := range r.Participants() {
		if c := s.connFor(p.ID()); c != nil {
			_ = c.send(msg{Type: msgEnded, Reason: "call ended"})
		}
		s.removeParticipant(roomID, p.ID())
	}
}

// outboundPending reports whether roomID's outbound call is still being placed
// (its far end hasn't answered). An agent joining such a room isn't answering
// anything: a click-to-call is answered when the customer picks up.
func (s *Server) outboundPending(roomID room.RoomID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, connected := s.outboundLegs[roomID]
	return s.dialing[roomID] && !connected
}

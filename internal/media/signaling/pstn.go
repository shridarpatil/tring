package signaling

// pstn.go — inbound PHONE calls on the media plane (Phase 4c).
//
// The phone side is a PLUG (room.Plug — FreeSWITCH today, anything
// tomorrow): an inbound call arrives as an InboundCall, this server resolves the
// dialed DID to a tenant + IVR flow, answers, and attaches the leg to a room as
// a bidirectional port (sfu.AttachPort). From there the call is EXACTLY a web
// call: the same IVR runs (local or brain-driven), transfer rings web agents
// into the room, the caller's keypresses ride the same DTMF path. No FS concept
// crosses this file — only the plug contract.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
)

// DIDResolver maps an inbound dialed number to its tenant + attached flow
// (internal/flowstore implements it).
type DIDResolver interface {
	ResolveDID(ctx context.Context, did string) (uuid.UUID, string, error)
}

// AttachPstn wires a phone plug into the server: inbound calls become rooms
// running the IVR. Call once at startup (cmd/gateway) when a plug is configured.
func (s *Server) AttachPstn(gw room.Plug, resolve DIDResolver) {
	s.pstnGW = gw
	s.pstnDID = resolve
	gw.OnInbound(func(ic room.InboundCall) { go s.handleInboundPhone(ic) })
	s.log.Info("pstn plug attached")
}

// AttachOriginator wires agent-initiated OUTBOUND dialing (msgDial,
// outbound.go) into the server: o places/hangs up the carrier leg, t resolves
// which trunk(s) a tenant dials through. Call once at startup (cmd/gateway)
// alongside AttachPstn, before the server serves — both nil until this is
// called, so outbound dialing stays disabled until wired.
func (s *Server) AttachOriginator(o Originator, t TrunkResolver) {
	s.cfg.Originator = o
	s.cfg.Trunks = t
	s.log.Info("originator attached")
}

// handleInboundPhone runs one phone call: resolve → answer → port → IVR → end.
func (s *Server) handleInboundPhone(ic room.InboundCall) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	tenant, flowID, err := s.pstnDID.ResolveDID(ctx, ic.To)
	cancel()
	if err != nil || flowID == "" {
		s.log.Warn("pstn: no flow for DID, rejecting", "did", ic.To, "err", err)
		_ = s.pstnGW.Reject(ic.CallID)
		return
	}

	leg, err := s.pstnGW.Answer(ic.CallID)
	if err != nil {
		s.log.Error("pstn: answer failed", "call", ic.CallID, "err", err)
		return
	}
	roomID := room.RoomID(tenant.String() + "/pstn-" + string(ic.CallID))

	s.mu.Lock()
	s.pstnCalls[roomID] = ic.CallID
	s.mu.Unlock()

	if err := s.sfu.AttachPort(roomID, leg.Source, leg.Write); err != nil {
		s.log.Error("pstn: attach port failed", "room", roomID, "err", err)
		_ = s.pstnGW.Hangup(ic.CallID)
		s.forgetPstn(roomID)
		return
	}

	// Open the CDR (and the caller's recording leg when the tenant records) —
	// the same calls-table row a FreeSWITCH-path call gets.
	if s.cfg.Calls != nil {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		if sink := s.cfg.Calls.Started(cctx, tenant, string(roomID), ic.From, ic.To, "phone", "", leg.Source.ID()); sink != nil {
			s.sfu.AddSink(roomID, leg.Source.ID(), sink)
		}
		ccancel()
	}

	// Run the IVR locally — the brain is embedded in-process. The caller's number
	// rides as the name/label the agent sees on a transfer ring.
	s.startIVR(roomID, flowID, ic.From, map[string]string{"caller": ic.From, "did": ic.To})
	s.log.Info("pstn: call attached", "room", roomID, "did", ic.To, "from", ic.From, "flow", flowID)

	// Pump the caller's keypresses into the same DTMF path a browser uses, and
	// tear everything down when the carrier side hangs up.
	for {
		select {
		case d := <-leg.DTMF:
			s.routeDTMF(roomID, d)
		case <-leg.Done:
			s.endPstnRoom(roomID)
			return
		}
	}
}

// endPstnRoom tears down everything room-side after the phone leg ended: the
// IVR (both paths), any agent still in the room (they get a terminal `ended`),
// and the port.
func (s *Server) endPstnRoom(roomID room.RoomID) {
	s.forgetPstn(roomID)
	s.endTransfer(roomID) // stop any blind-transfer ring the caller was waiting on
	s.stopIVR(roomID)
	// The leg ended on its own unless we hung it up (hangupPstnIfAbandoned
	// recorded "system", or the agent's leave recorded "agent"). Settle BEFORE
	// hanging up an IVR-dialed callee, whose Done would otherwise race in and
	// settle the call as "callee_hangup".
	if cause := s.endCauseOr(roomID, "caller_hangup"); s.cfg.Calls != nil {
		s.cfg.Calls.Ended(string(roomID), cause)
	}
	// A callee the IVR dialed (ivrdial.go) is left talking to nobody — hang it
	// up; its Done then runs endOutboundRoom.
	s.mu.Lock()
	calleePID, hasCallee := s.outboundLegs[roomID]
	s.mu.Unlock()
	if hasCallee && s.cfg.Originator != nil {
		_ = s.cfg.Originator.HangupOutbound(calleePID)
	}
	if r, ok := s.rooms.Get(roomID); ok {
		for _, p := range r.Participants() {
			if c := s.connFor(p.ID()); c != nil {
				_ = c.send(msg{Type: msgEnded, Reason: "caller hung up"})
			}
			s.removeParticipant(roomID, p.ID())
		}
	}
	// A call that ended in its IVR had no participant to leave, so routeEvent
	// never closed its supervisors.
	s.closeSupervisors(roomID)
	s.log.Info("pstn: room ended", "room", roomID)
}

// hangupPstnIfAbandoned hangs up the phone leg when its room has no human
// participants left — the agent hung up, or the flow ended with nobody joining
// (e.g. a hangup node / no-answer with no fallback). The leg's Done then fires
// and endPstnRoom completes the teardown. No-op for non-pstn rooms.
func (s *Server) hangupPstnIfAbandoned(roomID room.RoomID) {
	s.mu.Lock()
	callID, isPstn := s.pstnCalls[roomID]
	s.mu.Unlock()
	if !isPstn {
		return
	}
	if r, ok := s.rooms.Get(roomID); ok && len(r.Participants()) > 0 {
		return // an agent is (still) on the call
	}
	s.mu.Lock()
	_, hasCallee := s.outboundLegs[roomID]
	s.mu.Unlock()
	if hasCallee {
		return // a number the IVR dialed (ivrdial.go) is serving the caller
	}
	// Also no IVR running? Then nothing is serving the caller — hang up. (While
	// the IVR runs, the room legitimately has zero human participants.)
	if s.brain.Running(roomID) {
		return
	}
	s.log.Info("pstn: no one serving the caller, hanging up", "room", roomID)
	s.setHangupBy(roomID, "system") // no-op if the agent's leave was recorded first
	_ = s.pstnGW.Hangup(callID)
}

// phoneLive reports whether roomID's inbound phone leg is still connected.
func (s *Server) phoneLive(roomID room.RoomID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pstnCalls[roomID]
	return ok
}

func (s *Server) forgetPstn(roomID room.RoomID) {
	s.mu.Lock()
	delete(s.pstnCalls, roomID)
	s.mu.Unlock()
}

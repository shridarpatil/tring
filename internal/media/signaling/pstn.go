package signaling

// pstn.go — inbound PHONE calls on the media plane (Phase 4c).
//
// The phone side is a PLUG (room.PstnGateway — FreeSWITCH today, anything
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

	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/room"
)

// DIDResolver maps an inbound dialed number to its tenant + attached flow
// (internal/flowstore implements it).
type DIDResolver interface {
	ResolveDID(ctx context.Context, did string) (uuid.UUID, string, error)
}

// AttachPstn wires a phone plug into the server: inbound calls become rooms
// running the IVR. Call once at startup (cmd/gateway) when a plug is configured.
func (s *Server) AttachPstn(gw room.PstnGateway, resolve DIDResolver) {
	s.pstnGW = gw
	s.pstnDID = resolve
	gw.OnInbound(func(ic room.InboundCall) { go s.handleInboundPhone(ic) })
	s.log.Info("pstn plug attached")
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

	// Same IVR split as a web call: brain when the control channel is up, local
	// fallback otherwise. The caller's number rides as the name/label the agent
	// sees on a transfer ring.
	if s.controlUp() {
		_ = s.control.Send(control.Message{
			Type:   control.EvCallStarted,
			Tenant: tenant.String(),
			Room:   string(roomID),
			FlowID: flowID,
			Name:   ic.From,
		})
	} else {
		s.startIVR(roomID, flowID, ic.From)
	}
	s.log.Info("pstn: call attached", "room", roomID, "did", ic.To, "from", ic.From, "flow", flowID)

	// Pump the caller's keypresses into the same DTMF path a browser uses, and
	// tear everything down when the carrier side hangs up.
	for {
		select {
		case d := <-leg.DTMF:
			if s.controlUp() {
				_ = s.control.Send(control.Message{Type: control.EvDTMF, Room: string(roomID), Digit: d})
			} else {
				s.routeDTMF(roomID, d)
			}
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
	if s.controlUp() {
		_ = s.control.Send(control.Message{Type: control.EvCallEnded, Room: string(roomID)})
		s.edgeStopVoice(roomID)
	}
	s.stopIVR(roomID)
	if r, ok := s.rooms.Get(roomID); ok {
		for _, p := range r.Participants() {
			if c := s.connFor(p.ID()); c != nil {
				_ = c.send(msg{Type: msgEnded, Reason: "caller hung up"})
			}
			s.removeParticipant(roomID, p.ID())
		}
	}
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
	// Also no IVR running? Then nothing is serving the caller — hang up. (While
	// the IVR runs, the room legitimately has zero human participants.)
	s.mu.Lock()
	ivrRunning := s.ivr[roomID] != nil || s.edge[roomID] != nil
	s.mu.Unlock()
	if ivrRunning {
		return
	}
	s.log.Info("pstn: no one serving the caller, hanging up", "room", roomID)
	_ = s.pstnGW.Hangup(callID)
}

func (s *Server) forgetPstn(roomID room.RoomID) {
	s.mu.Lock()
	delete(s.pstnCalls, roomID)
	s.mu.Unlock()
}

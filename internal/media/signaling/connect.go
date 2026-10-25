package signaling

// connect.go — two-number connect (click-to-call / callback, the API's POST
// /v1/calls/connect): ring the agent's phone and, once it answers, the
// client's, then join both legs into one room as ports. The call is then like
// any other media-plane call: one CDR, the same recording (agent left, client
// right), supervisable. The agent hears the tenant's hold music while the
// client rings. Either side hanging up ends the call; a number that doesn't
// answer fails it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dial"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/room"
)

// connectRingTimeout bounds how long each number rings.
const connectRingTimeout = 60 * time.Second

// errConnectNoCDR: the call row couldn't be created, so the call isn't placed
// (it would be invisible).
var errConnectNoCDR = errors.New("could not record the call")

// Connect validates both numbers, opens the call row (ringing) and places the
// call in the background, returning the call's id. Errors are returned only for
// calls never placed; a number that doesn't answer fails the call row instead.
func (s *Server) Connect(ctx context.Context, tenant uuid.UUID, agentNumber, clientNumber string) (uuid.UUID, error) {
	if s.cfg.Originator == nil || s.cfg.Trunks == nil || s.cfg.Calls == nil {
		return uuid.Nil, errDialUnavailable
	}
	agent, err := dial.India(agentNumber)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: agent_number: %v", errDialInvalidNumber, err)
	}
	client, err := dial.India(clientNumber)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: client_number: %v", errDialInvalidNumber, err)
	}
	if trunks, err := s.cfg.Trunks.OutboundTrunks(ctx, tenant); err != nil || len(trunks) == 0 {
		return uuid.Nil, fmt.Errorf("%w: %v", errDialNoTrunk, err)
	}

	// from = the agent (rung first), to = the client.
	roomID := room.RoomID(tenant.String() + "/connect-" + uuid.NewString())
	s.cfg.Calls.StartedOutbound(ctx, tenant, string(roomID), agent, client, "", "")
	callID := s.cfg.Calls.CallID(string(roomID))
	if callID == uuid.Nil {
		return uuid.Nil, errConnectNoCDR
	}
	go s.runConnect(roomID, tenant, agent, client)
	return callID, nil
}

// runConnect places a Connect call: agent first, then client, then waits for
// either side to hang up.
func (s *Server) runConnect(roomID room.RoomID, tenant uuid.UUID, agent, client string) {
	octx, cancel := context.WithTimeout(context.Background(), connectRingTimeout)
	agentLeg, callerID, _, err := s.originateOut(octx, tenant, agent, "", nil)
	cancel()
	if err != nil {
		s.log.Warn("connect: agent's number not answered", "room", roomID, "to", agent, "err", err)
		s.cfg.Calls.Failed(string(roomID), "agent_not_answered")
		return
	}
	agentPID := agentLeg.Source.ID()
	tagParticipant(agentPID, "agent")
	if err := s.sfu.AttachPort(roomID, agentLeg.Source, agentLeg.Write); err != nil {
		s.log.Error("connect: attach agent failed", "room", roomID, "err", err)
		s.cfg.Calls.Failed(string(roomID), "system")
		s.endConnect(roomID, agentPID)
		return
	}
	s.log.Info("connect: agent answered, ringing client", "room", roomID, "to", client, "caller_id", callerID)

	// The agent hears hold music while the client rings, and hanging up stops
	// the ring.
	stopMusic := s.playHold(roomID, s.tenantHoldMusic(roomID))
	octx, cancel = context.WithTimeout(context.Background(), connectRingTimeout)
	go func() {
		select {
		case <-agentLeg.Done:
			cancel()
		case <-octx.Done():
		}
	}()
	clientLeg, _, _, err := s.originateOut(octx, tenant, client, "", nil)
	cancel()
	stopMusic()
	if err != nil || closed(agentLeg.Done) {
		cause := "client_not_answered"
		if closed(agentLeg.Done) {
			cause = "agent_hangup"
		}
		s.log.Warn("connect: client not connected", "room", roomID, "to", client, "cause", cause, "err", err)
		s.cfg.Calls.Failed(string(roomID), cause)
		if clientLeg != nil {
			s.endConnect(roomID, agentPID, clientLeg.Source.ID())
		} else {
			s.endConnect(roomID, agentPID)
		}
		return
	}
	clientPID := clientLeg.Source.ID()
	tagParticipant(clientPID, "callee")
	if err := s.sfu.AttachPort(roomID, clientLeg.Source, clientLeg.Write); err != nil {
		s.log.Error("connect: attach client failed", "room", roomID, "err", err)
		s.cfg.Calls.Failed(string(roomID), "system")
		s.endConnect(roomID, agentPID, clientPID)
		return
	}

	// Both connected: the call is answered and recording starts — the client
	// as the customer side, the agent's phone as the agent side.
	if sink := s.cfg.Calls.Customer(string(roomID), clientPID); sink != nil {
		s.sfu.AddSink(roomID, clientPID, sink)
	}
	if sink := s.cfg.Calls.Answered(string(roomID), agentPID, ""); sink != nil {
		s.sfu.AddSink(roomID, agentPID, sink)
	}
	s.log.Info("connect: connected", "room", roomID)

	cause := "client_hangup"
	select {
	case <-agentLeg.Done:
		cause = "agent_hangup"
	case <-clientLeg.Done:
	}
	s.cfg.Calls.Ended(string(roomID), cause) // settle before hanging up the other side
	s.endConnect(roomID, agentPID, clientPID)
}

// endConnect hangs up and detaches a Connect call's legs (a leg that already
// ended is a no-op hangup) and ends any supervisor sessions.
func (s *Server) endConnect(roomID room.RoomID, pids ...room.ParticipantID) {
	for _, pid := range pids {
		_ = s.cfg.Originator.HangupOutbound(pid)
		s.sfu.DetachPort(roomID, pid)
		logging.ClearParticipant(string(pid))
	}
	s.closeSupervisors(roomID)
	s.log.Info("connect: room ended", "room", roomID)
}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// controlAudience is the audience of the API's tokens for the control
// endpoints (internal/api mints them per request, signed with the shared JWT
// secret).
const controlAudience = "tring.control"

type controlClaims struct {
	TenantID uuid.UUID `json:"tenant"`
	jwt.RegisteredClaims
}

// ControlHandler serves the API-only control endpoints. cmd/gateway serves it
// on its own loopback listener (gateway_control_addr), never on the public one.
func (s *Server) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /control/connect", s.handleControlConnect)
	return mux
}

// handleControlConnect is POST /control/connect {agent_number, client_number}
// → 201 {call_id}. The tenant comes from the token.
func (s *Server) handleControlConnect(w http.ResponseWriter, r *http.Request) {
	var c controlClaims
	raw := bearer(r)
	if raw == "" {
		controlError(w, http.StatusUnauthorized, "missing token")
		return
	}
	if _, err := jwt.ParseWithClaims(raw, &c, hmacKey(s.cfg.JWTSecret),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(controlAudience),
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
	); err != nil {
		controlError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	var req struct {
		AgentNumber  string `json:"agent_number"`
		ClientNumber string `json:"client_number"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		controlError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	callID, err := s.Connect(r.Context(), c.TenantID, req.AgentNumber, req.ClientNumber)
	switch {
	case errors.Is(err, errDialInvalidNumber):
		controlError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, errDialNoTrunk):
		controlError(w, http.StatusUnprocessableEntity, "no outbound trunk")
		return
	case errors.Is(err, errDialUnavailable):
		controlError(w, http.StatusServiceUnavailable, "outbound calling not available")
		return
	case err != nil:
		s.log.Error("connect: call not placed", "tenant", c.TenantID, "err", err)
		controlError(w, http.StatusInternalServerError, "call not placed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]uuid.UUID{"call_id": callID})
}

func controlError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

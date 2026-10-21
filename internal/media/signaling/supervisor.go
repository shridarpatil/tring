package signaling

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/media/room"
)

// partRole is a signaling-only classification (the room/sfu stay role-blind). It
// drives whisper targeting (which pids are agents) and the agent-facing
// supervisor indicator.
type partRole int

const (
	roleVisitor partRole = iota
	roleAgent
	roleSupervisor
)

// SupervisorLog records supervisor sessions for audit (internal/callcdr
// implements it). Best-effort; a nil SupervisorLog disables audit.
type SupervisorLog interface {
	SuperviseStart(ctx context.Context, tenant uuid.UUID, callRoomID, userID, name, mode string) string
	SuperviseMode(callRoomID, name, mode string)
	SuperviseEnd(sessionID, callRoomID, name string, modes []string)
}

// validSuperviseMode reports whether m is a recognized supervisor mode.
func validSuperviseMode(m string) bool {
	switch m {
	case "monitor", "whisper", "barge":
		return true
	}
	return false
}

// superviseAudience maps a mode to an SFU audience: monitor → nobody, whisper →
// the agent pids, barge → everyone. Unknown modes fall back to monitor (silent).
func superviseAudience(mode string, agents []room.ParticipantID) (all bool, allowed []room.ParticipantID) {
	switch mode {
	case "barge":
		return true, nil
	case "whisper":
		return false, agents
	default: // monitor / unknown
		return false, nil
	}
}

// setRole / clearRole manage the signaling role map (guarded by s.mu).
func (s *Server) setRole(pid room.ParticipantID, role partRole) {
	s.mu.Lock()
	if s.roles == nil {
		s.roles = make(map[room.ParticipantID]partRole)
	}
	s.roles[pid] = role
	s.mu.Unlock()
}

func (s *Server) clearRole(pid room.ParticipantID) {
	s.mu.Lock()
	delete(s.roles, pid)
	s.mu.Unlock()
}

// agentPIDs returns the pids of agent-role participants currently in the room —
// the whisper target set.
func (s *Server) agentPIDs(roomID room.RoomID) []room.ParticipantID {
	r, ok := s.rooms.Get(roomID)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []room.ParticipantID
	for _, p := range r.Participants() {
		if s.roles[p.ID()] == roleAgent {
			out = append(out, p.ID())
		}
	}
	return out
}

// notifyAgentsSupervisor pushes the supervisor indicator (present/mode/name) to
// every agent-role leg in the room. Customer legs never receive it.
func (s *Server) notifyAgentsSupervisor(roomID room.RoomID, present bool, mode, name string) {
	r, ok := s.rooms.Get(roomID)
	if !ok {
		return
	}
	for _, p := range r.Participants() {
		s.mu.Lock()
		isAgent := s.roles[p.ID()] == roleAgent
		c := s.conns[p.ID()]
		s.mu.Unlock()
		if isAgent && c != nil {
			_ = c.send(msg{Type: msgSupervisor, Present: present, Mode: mode, Name: name})
		}
	}
}

// registerSupervisor / unregisterSupervisor track supervisor conns per room so
// they can be closed when the call ends.
func (s *Server) registerSupervisor(roomID room.RoomID, pid room.ParticipantID, c *conn) {
	s.mu.Lock()
	if s.supers == nil {
		s.supers = make(map[room.RoomID]map[room.ParticipantID]*conn)
	}
	if s.supers[roomID] == nil {
		s.supers[roomID] = make(map[room.ParticipantID]*conn)
	}
	s.supers[roomID][pid] = c
	s.mu.Unlock()
}

func (s *Server) unregisterSupervisor(roomID room.RoomID, pid room.ParticipantID) {
	s.mu.Lock()
	if m := s.supers[roomID]; m != nil {
		delete(m, pid)
		if len(m) == 0 {
			delete(s.supers, roomID)
		}
	}
	s.mu.Unlock()
}

// supervisorsRemain reports whether any supervisor is still watching roomID.
func (s *Server) supervisorsRemain(roomID room.RoomID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.supers[roomID]) > 0
}

// closeSupervisors ends every supervisor watching a room (called when the call
// itself ends). Each closed socket runs supervisorReadLoop's teardown.
func (s *Server) closeSupervisors(roomID room.RoomID) {
	s.mu.Lock()
	m := s.supers[roomID]
	conns := make([]*conn, 0, len(m))
	for _, c := range m {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.send(msg{Type: msgEnded, Reason: "call ended"})
		_ = c.Close()
	}
}

// handleSupervisor joins a console supervisor to a live room as an SFU-only
// participant: subscribed to everyone, audience-gated outbound (monitor by
// default). It is NOT a room.Manager participant, so it generates no membership
// events to the caller/agent — the customer never learns of it; the agent is
// told explicitly via notifyAgentsSupervisor.
func (s *Server) handleSupervisor(c *conn, princ *principal, first msg, tenant string) {
	defer func() { _ = c.Close() }()

	if first.Type != msgJoin {
		_ = c.send(msg{Type: msgError, Reason: "expected join"})
		return
	}
	// Room is pinned by the TOKEN, never the join body.
	roomID := room.RoomID(tenant + "/" + princ.room)

	// Burn the single-use jti (fail-closed, same store as visitor tokens).
	if err := s.consumeVisitorToken(context.Background(), princ.jti, princ.exp); err != nil {
		s.log.Warn("supervisor token rejected", "err", err)
		_ = c.send(msg{Type: msgError, Reason: "token rejected"})
		return
	}

	// Register BEFORE the liveness check (not after) so closeSupervisors can
	// find and close us if the room empties in the gap between the check and
	// registration — otherwise a call ending right here would never kick us.
	pid := room.ParticipantID("sup-" + uuid.NewString())
	c.pid = pid
	s.setRole(pid, roleSupervisor)
	s.mu.Lock()
	s.conns[pid] = c
	s.mu.Unlock()
	s.registerSupervisor(roomID, pid, c)

	// The call must be live on THIS gateway (media is process-bound). Checked
	// AFTER registration: if the room was already empty, closeSupervisors never
	// ran (nothing was registered yet) so we catch it here instead.
	r, ok := s.rooms.Get(roomID)
	if !ok || len(r.Participants()) == 0 {
		_ = c.send(msg{Type: msgError, Reason: "call not active"})
		s.teardownSupervisor(roomID, pid)
		return
	}

	peer, err := s.sfu.Join(roomID, pid, c)
	if err != nil {
		s.log.Error("supervisor sfu join failed", "err", err, "room", roomID)
		s.teardownSupervisor(roomID, pid)
		return
	}
	c.peer = peer

	// Monitor by default: audience = nobody. Set BEFORE the supervisor's mic can
	// publish, so onPublish never wires the caller/agent to the supervisor.
	s.sfu.SetAudience(roomID, pid, false, nil)

	_ = c.send(msg{Type: msgJoined, Participant: string(pid), ICEServers: s.iceServers(tenant)})
	// Show the supervisor who's on the call.
	for _, other := range r.Participants() {
		if other.ID() != pid {
			_ = c.send(msg{Type: msgPeerJoined, Participant: string(other.ID()), Name: other.Name()})
		}
	}
	peer.Negotiate()

	// Tell the agent(s) a supervisor is present; publish the dashboard event.
	s.notifyAgentsSupervisor(roomID, true, "monitor", princ.name)
	s.publishSupervised(tenant, roomID, "monitor", princ.name)

	// Audit start. userID is the JWT subject (the console user), NOT the token
	// jti — supervisor_user_id is a FK into users(id), and the jti is a random
	// per-token uuid the DB would reject.
	var sessionID string
	modes := []string{"monitor"}
	if s.cfg.Supervisors != nil {
		startCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		sessionID = s.cfg.Supervisors.SuperviseStart(startCtx, princ.tenant, string(roomID), princ.userID, princ.name, "monitor")
		cancel()
	}

	s.log.Info("supervisor joined", "room", roomID, "participant", pid, "name", princ.name)
	s.supervisorReadLoop(c, roomID, pid, tenant, princ.name, &modes)

	s.teardownSupervisor(roomID, pid)
	if s.cfg.Supervisors != nil && sessionID != "" {
		s.cfg.Supervisors.SuperviseEnd(sessionID, string(roomID), princ.name, modes)
	}
	// Only clear the indicator if no OTHER supervisor is still watching this room.
	if !s.supervisorsRemain(roomID) {
		s.notifyAgentsSupervisor(roomID, false, "", princ.name)
		s.publishSupervised(tenant, roomID, "", princ.name) // present=false via empty mode
	}
}

// supervisorReadLoop handles a supervisor socket: SDP/ICE plus live mode
// switches. Appends each new mode to *modes for the audit trail.
func (s *Server) supervisorReadLoop(c *conn, roomID room.RoomID, pid room.ParticipantID, tenant, name string, modes *[]string) {
	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		m, err := c.read()
		if err != nil {
			return
		}
		switch m.Type {
		case msgAnswer:
			if c.peer != nil {
				if err := c.peer.HandleAnswer(m.SDP); err != nil {
					s.log.Warn("supervisor answer", "err", err)
				}
			}
		case msgICE:
			if c.peer != nil && len(m.Candidate) > 0 {
				if init := webrtcICE(m.Candidate); init != nil {
					if err := c.peer.HandleCandidate(*init); err != nil {
						s.log.Warn("supervisor candidate", "err", err)
					}
				}
			}
		case msgSupervise:
			if !validSuperviseMode(m.Mode) {
				_ = c.send(msg{Type: msgError, Reason: "invalid mode"})
				continue
			}
			all, allowed := superviseAudience(m.Mode, s.agentPIDs(roomID))
			s.sfu.SetAudience(roomID, pid, all, allowed)
			// changed is computed BEFORE the append, against the current (last)
			// mode — clicking the same mode logs nothing on the timeline.
			changed := len(*modes) == 0 || (*modes)[len(*modes)-1] != m.Mode
			*modes = append(*modes, m.Mode)
			s.notifyAgentsSupervisor(roomID, true, m.Mode, name)
			s.publishSupervised(tenant, roomID, m.Mode, name)
			if changed && s.cfg.Supervisors != nil {
				s.cfg.Supervisors.SuperviseMode(string(roomID), name, m.Mode)
			}
		case msgPing:
			// keepalive; reply so a proxy sees traffic both ways (see readLoop)
			_ = c.send(msg{Type: msgPong})
		case msgBye:
			return
		default:
			s.log.Warn("unknown supervisor message", "type", m.Type)
		}
	}
}

// teardownSupervisor removes the supervisor's SFU peer + bookkeeping. Idempotent.
func (s *Server) teardownSupervisor(roomID room.RoomID, pid room.ParticipantID) {
	s.sfu.Leave(roomID, pid)
	s.unregisterSupervisor(roomID, pid)
	s.clearRole(pid)
	s.mu.Lock()
	if s.conns[pid] != nil {
		delete(s.conns, pid)
	}
	s.mu.Unlock()
}

// publishSupervised emits a dashboard call.supervised event. An empty mode means
// the supervisor left (present=false).
func (s *Server) publishSupervised(tenant string, roomID room.RoomID, mode, name string) {
	if s.cfg.Events == nil {
		return
	}
	s.cfg.Events.Publish(context.Background(), tenant, events.TypeCallSupervised, map[string]any{
		"room":       string(roomID),
		"mode":       mode,
		"present":    mode != "",
		"supervisor": name,
	})
}

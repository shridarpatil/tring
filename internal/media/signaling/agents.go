package signaling

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/ivrdrive"
	"github.com/whatomate/tring/internal/media/room"
)

// ringTimeout bounds how long a transfer rings before giving up (no_answer).
const ringTimeout = 30 * time.Second

// handleAgentPresence runs an agent's presence socket: it registers the agent as
// available, then blocks reading (keepalive / close) while it can receive
// `incoming` pushes from a transfer ring. The agent answers by opening a normal
// `join` on another socket. Deregisters on disconnect.
func (s *Server) handleAgentPresence(c *conn, tenant, sip string) {
	s.mu.Lock()
	if s.agents[tenant] == nil {
		s.agents[tenant] = make(map[string]*conn)
	}
	s.agents[tenant][sip] = c
	s.mu.Unlock()
	_ = c.send(msg{Type: msgReady})
	s.log.Info("agent online", "tenant", tenant, "sip", sip)
	// Tell the brain (it owns routing/presence in 2c).
	if s.controlUp() {
		_ = s.control.Send(control.Message{Type: control.EvAgentOnline, Tenant: tenant, Sip: sip})
	}

	defer func() {
		s.mu.Lock()
		if s.agents[tenant][sip] == c {
			delete(s.agents[tenant], sip)
		}
		s.mu.Unlock()
		_ = c.Close()
		s.log.Info("agent offline", "tenant", tenant, "sip", sip)
		if s.controlUp() {
			_ = s.control.Send(control.Message{Type: control.EvAgentOffline, Tenant: tenant, Sip: sip})
		}
	}()

	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		if _, err := c.read(); err != nil {
			return
		}
		// Presence socket only takes keepalives; calls happen on a join socket.
	}
}

// Ring implements ivrdrive.Agents: it rings matching agents into `room` and
// blocks until one joins (a call `join` to that room closes the pending-ring
// channel → "completed") or ringTimeout elapses / there are no agents
// ("no_answer"). `room` is the tenant-namespaced id ("<tenant>/<name>").
func (s *Server) Ring(ctx context.Context, roomID string, t ivrdrive.Target) (string, error) {
	tenant, name, ok := strings.Cut(roomID, "/")
	if !ok {
		return "no_answer", nil
	}

	// Resolve which SIPs to ring (DB lookup OUTSIDE the registry lock):
	//   - a specific agent_sip → just that agent
	//   - a team → its members (parity with the FreeSWITCH executor)
	//   - neither → every online agent of the tenant (ring-all)
	var sips []string
	ringAll := true
	if t.AgentSIP != "" {
		sips, ringAll = []string{t.AgentSIP}, false
	} else if t.Team != "" && s.cfg.Teams != nil {
		ringAll = false
		if tid, err := uuid.Parse(tenant); err == nil {
			if m, err := s.cfg.Teams.TeamSIPs(ctx, tid, t.Team); err == nil {
				sips = m
			} else {
				s.log.Warn("transfer: team resolve failed", "team", t.Team, "err", err)
			}
		}
	}

	// Pick the online sockets for those SIPs (or all online for ring-all).
	s.mu.Lock()
	var targets []*conn
	if ringAll {
		for _, c := range s.agents[tenant] {
			targets = append(targets, c)
		}
	} else {
		for _, sip := range sips {
			if c := s.agents[tenant][sip]; c != nil {
				targets = append(targets, c)
			}
		}
	}
	if len(targets) == 0 {
		s.mu.Unlock()
		return "no_answer", nil
	}
	// Register the pending ring BEFORE pushing, so the agent's join can't race
	// ahead of the waiter.
	ring := make(chan struct{})
	s.rings[room.RoomID(roomID)] = ring
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.rings[room.RoomID(roomID)] == ring {
			delete(s.rings, room.RoomID(roomID))
		}
		s.mu.Unlock()
	}()

	// Tell the agents to join `name` (un-namespaced — their own join re-namespaces
	// by their tenant, which matches since they're the same tenant).
	for _, c := range targets {
		_ = c.send(msg{Type: msgIncoming, Room: name, From: t.Label})
	}

	select {
	case <-ring:
		return "completed", nil
	case <-time.After(ringTimeout):
		return "no_answer", nil
	case <-ctx.Done():
		return "no_answer", ctx.Err()
	}
}

// signalRing completes a pending transfer ring for a room (an agent joined).
func (s *Server) signalRing(roomID room.RoomID) {
	s.mu.Lock()
	ring := s.rings[roomID]
	if ring != nil {
		delete(s.rings, roomID)
	}
	s.mu.Unlock()
	if ring != nil {
		close(ring)
	}
}

// ensure the server satisfies the IVR's agent-ring seam.
var _ ivrdrive.Agents = (*Server)(nil)

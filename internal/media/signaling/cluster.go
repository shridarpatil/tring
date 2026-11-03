package signaling

import (
	"context"
	"strings"

	"github.com/shridarpatil/tring/internal/cluster"
)

// ClusterSnapshot is this node's full contribution to the cluster state, taken
// right before each heartbeat. Rewriting it whole each time is what repairs any
// immediate Set* write that failed or landed out of order.
func (s *Server) ClusterSnapshot() cluster.Snapshot {
	// Outside s.mu: the room manager has its own lock.
	legs := s.activeCalls()

	s.mu.Lock()
	defer s.mu.Unlock()
	snap := cluster.Snapshot{
		Legs:     legs,
		Draining: s.draining.Load(),
		Online:   make(map[string][]string, len(s.agents)),
		Away:     make(map[string][]string, len(s.agents)),
		Counts: map[cluster.Kind]map[string]int{
			cluster.OnCall: countsOf(s.onCall),
			cluster.Dials:  countsOf(s.agentDials),
		},
	}
	for tenant, socks := range s.agents {
		// Empty slices too, so the heartbeat clears a tenant's sets the moment
		// its last agent leaves instead of waiting for them to expire.
		online, away := make([]string, 0, len(socks)), []string{}
		for sip := range socks {
			online = append(online, sip)
		}
		snap.Online[tenant], snap.Away[tenant] = online, away
	}
	for k, on := range s.away {
		tenant, sip, ok := strings.Cut(k, "|")
		if !on || !ok || s.agents[tenant][sip] == nil {
			continue
		}
		snap.Away[tenant] = append(snap.Away[tenant], sip)
	}
	return snap
}

// countsOf copies the positive entries of a "tenant|sip" → n map.
func countsOf(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, n := range m {
		if n > 0 {
			out[k] = n
		}
	}
	return out
}

// pushRing sends msgIncoming {room, from: label, auto} to sip's presence socket:
// locally when node == "", else as a MsgRing to that node. false if neither
// worked. Callers must not hold s.mu: the remote path is Redis I/O.
func (s *Server) pushRing(tenant, sip, node, roomName, label string, auto bool) bool {
	if node == "" {
		return s.pushLocal(tenant, sip, msg{Type: msgIncoming, Room: roomName, From: label, Auto: auto})
	}
	err := s.cfg.Cluster.Send(context.Background(), node, cluster.Msg{
		Type: cluster.MsgRing, Tenant: tenant, SIP: sip, Room: roomName, Label: label, Auto: auto,
	})
	if err != nil {
		// Loud: with the bus down no agent on another node can be rung, and the
		// router only sees unanswered rings.
		s.log.Error("ring bus: ring not delivered", "tenant", tenant, "sip", sip, "node", node, "room", roomName, "err", err)
		return false
	}
	return true
}

// pushCancel withdraws a ring pushRing delivered, on the same path. Best-effort.
func (s *Server) pushCancel(tenant, sip, node, roomName string) {
	if node == "" {
		s.pushLocal(tenant, sip, msg{Type: msgCancelRing, Room: roomName})
		return
	}
	err := s.cfg.Cluster.Send(context.Background(), node, cluster.Msg{
		Type: cluster.MsgRingCancel, Tenant: tenant, SIP: sip, Room: roomName,
	})
	if err != nil {
		s.log.Error("ring bus: ring cancel not delivered", "tenant", tenant, "sip", sip, "node", node, "room", roomName, "err", err)
	}
}

// pushLocal sends m to sip's presence socket on this node, if there is one.
func (s *Server) pushLocal(tenant, sip string, m msg) bool {
	s.mu.Lock()
	c := s.agents[tenant][sip]
	s.mu.Unlock()
	return c != nil && c.send(m) == nil
}

// remoteRingKey keys remoteRings: one ring of one agent for one room.
func remoteRingKey(tenant, roomName, sip string) string {
	return tenant + "/" + roomName + "|" + sip
}

// HandleClusterMsg is the ring bus handler (cluster.Run's handle). It runs on
// the bus's single reader goroutine, and go-redis drops messages once its
// buffer fills, so it never waits on anything but a local socket write.
func (s *Server) HandleClusterMsg(m cluster.Msg) {
	key := remoteRingKey(m.Tenant, m.Room, m.SIP)
	switch m.Type {
	case cluster.MsgRing:
		s.mu.Lock()
		c := s.agents[m.Tenant][m.SIP]
		if c != nil {
			// The sender owns the room: a decline from this agent goes back to it.
			s.remoteRings[key] = m.From
		}
		s.mu.Unlock()
		// No socket (agent just left): the room node sees no answer and moves on.
		if c != nil {
			_ = c.send(msg{Type: msgIncoming, Room: m.Room, From: m.Label, Auto: m.Auto})
		}
	case cluster.MsgRingCancel:
		s.mu.Lock()
		delete(s.remoteRings, key)
		c := s.agents[m.Tenant][m.SIP]
		s.mu.Unlock()
		if c != nil {
			_ = c.send(msg{Type: msgCancelRing, Room: m.Room})
		}
	case cluster.MsgDecline:
		// This node owns the room. Withdrawing the popup publishes a ring_cancel
		// back over the bus, so run it off the reader goroutine.
		go s.handleDecline(m.Tenant, m.SIP, m.Room)
	}
}

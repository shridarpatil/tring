package signaling

import (
	"context"
	"time"

	"github.com/shridarpatil/tring/internal/cluster"
)

// clusterReadTimeout bounds a cluster read on the ringing path: past it the
// decision falls back to this node's own agents rather than stall the call.
const clusterReadTimeout = time.Second

// agentView is one tenant's agents as a ringing decision should see them: the
// whole cluster when this node has live peers, else this node's own maps. Every
// reader of "is this agent online / free" goes through it so single-node and
// cluster mode can't drift apart.
type agentView struct {
	self   string                        // this node's id; states on it report node ""
	states map[string]cluster.AgentState // sip → state, online agents only
}

// agentView builds the view for tenant. The cluster read happens with no lock
// held; any failure (Redis down, no heartbeat yet) means the local maps, so a
// Redis outage never stops ringing agents connected here.
func (s *Server) agentView(ctx context.Context, tenant string) agentView {
	if c := s.cfg.Cluster; !c.Alone() {
		ctx, cancel := context.WithTimeout(ctx, clusterReadTimeout)
		states, err := c.Agents(ctx, tenant)
		cancel()
		if err == nil {
			s.mergeLocal(tenant, c.ID(), states)
			return agentView{self: c.ID(), states: states}
		}
		s.log.Warn("cluster agents read failed, using local agents", "tenant", tenant, "err", err)
	}
	return s.localAgentView(tenant)
}

// mergeLocal folds this node's own maps into a cluster read. Redis can lag
// what this node knows (an immediate write failed and the heartbeat hasn't
// rewritten it yet), and a busy or Away agent must never read as free. So a
// local call or Away wins, and an agent with a socket here is online even if
// Redis missed it. The cluster count already includes this node's share, hence
// max rather than sum (as in beginDial).
func (s *Server) mergeLocal(tenant, self string, states map[string]cluster.AgentState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sip := range s.agents[tenant] {
		if _, ok := states[sip]; !ok {
			states[sip] = cluster.AgentState{Node: self}
		}
	}
	for sip, st := range states {
		k := tenant + "|" + sip
		st.OnCall = max(st.OnCall, s.onCall[k])
		st.Dials = max(st.Dials, s.agentDials[k])
		if s.agents[tenant][sip] != nil && s.away[k] {
			st.Away = true
		}
		states[sip] = st
	}
}

// localAgentView is the view from this node's maps alone (today's semantics).
func (s *Server) localAgentView(tenant string) agentView {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make(map[string]cluster.AgentState, len(s.agents[tenant]))
	for sip := range s.agents[tenant] {
		k := tenant + "|" + sip
		states[sip] = cluster.AgentState{Away: s.away[k], OnCall: s.onCall[k], Dials: s.agentDials[k]}
	}
	return agentView{states: states}
}

// online reports whether the agent has a presence socket on some node.
func (v agentView) online(sip string) bool {
	_, ok := v.states[sip]
	return ok
}

// free reports whether the agent can be rung: online, on no call, not Away.
func (v agentView) free(sip string) bool {
	st, ok := v.states[sip]
	return ok && st.OnCall == 0 && !st.Away
}

// onCall reports whether the agent has an active call leg on any node.
func (v agentView) onCall(sip string) bool { return v.states[sip].OnCall > 0 }

// onlineSIPs lists every online agent (unordered, like the map it replaces).
func (v agentView) onlineSIPs() []string {
	out := make([]string, 0, len(v.states))
	for sip := range v.states {
		out = append(out, sip)
	}
	return out
}

// node is the node holding the agent's presence socket, "" when it is this one
// (or the view is local), so callers deliver locally without a bus hop.
func (v agentView) node(sip string) string {
	if n := v.states[sip].Node; n != v.self {
		return n
	}
	return ""
}

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
			return agentView{self: c.ID(), states: states}
		}
		s.log.Warn("cluster agents read failed, using local agents", "tenant", tenant, "err", err)
	}
	return s.localAgentView(tenant)
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

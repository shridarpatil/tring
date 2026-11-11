package signaling

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/brain"
	"github.com/shridarpatil/tring/internal/campaign"
	"github.com/shridarpatil/tring/internal/media/room"
)

func agentCampaignHarness(t *testing.T, members []string, results ...connectLeg) (*callHarness, *campaignMemory) {
	t.Helper()
	h := newCallHarness(t, results...)
	teams := shortTeams{members: map[string][]string{"sales": members}}
	h.srv.cfg.Teams = teams
	h.srv.agentRouter = brain.NewAgentRouter(teams, h.srv.router, h.srv, nil, h.srv.log)
	trunks := h.srv.cfg.Trunks.(fakeTrunkResolver)
	trunks.owned = map[string]bool{"+912265915000": true}
	h.srv.cfg.Trunks = trunks
	m := &campaignMemory{done: make(chan string, 1), a: campaign.Attempt{ID: uuid.New(), CampaignID: uuid.New(), Phone: "+919876543210", Payload: campaign.Payload{Config: campaign.Config{Type: "agent", Team: "sales", AgentWaitSeconds: 2, CallerID: "+912265915000", RingSeconds: 5, MaxSeconds: 10}, Prepared: campaign.Prepared{Team: "sales"}}}}
	h.srv.cfg.Campaigns = m
	// Presence registers Away until its availability read completes.
	deadline := time.Now().Add(time.Second)
	for {
		h.srv.mu.Lock()
		away := h.srv.away[h.tenant.String()+"|alice"]
		h.srv.mu.Unlock()
		if !away {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never became available")
		}
		time.Sleep(time.Millisecond)
	}
	return h, m
}

func startAgentCampaign(h *callHarness, m *campaignMemory) {
	h.srv.startPortCall()
	go h.srv.runCampaign(h.tenant, m.a)
}

func answerCampaign(t *testing.T, h *callHarness) net.Conn {
	t.Helper()
	in := h.readUntil(h.pres, msgIncoming)
	if in.Auto || !in.Phone {
		t.Fatalf("must ask the agent to accept a phone call: %+v", in)
	}
	select {
	case number := <-h.o.dialed:
		t.Fatalf("client dialed before agent accepted: %s", number)
	default:
	}
	c := h.dial()
	h.send(c, msg{Type: msgJoin, Room: in.Room})
	h.readUntil(c, msgJoined)
	return c
}

func campaignResult(t *testing.T, m *campaignMemory) string {
	t.Helper()
	select {
	case got := <-m.done:
		return got
	case <-time.After(4 * time.Second):
		t.Fatal("campaign did not finish")
		return ""
	}
}

func TestAgentCampaignCallsClientOnlyAfterTeamAgentAccepts(t *testing.T) {
	client := newConnectLeg("campaign-client")
	h, m := agentCampaignHarness(t, []string{"alice"}, client)
	startAgentCampaign(h, m)
	agent := answerCampaign(t, h)
	if got := <-h.o.dialed; got != m.a.Phone {
		t.Fatalf("dialed %s", got)
	}
	h.readUntil(agent, msgAnswered)
	close(client.done)
	if got := campaignResult(t, m); got != "completed:" {
		t.Fatal(got)
	}
	events := h.cl.snapshot()
	if len(events) != 4 || !strings.HasPrefix(events[1], "customer:") || !strings.HasPrefix(events[2], "answered:") {
		t.Fatalf("CDR answered before client: %v", events)
	}
	h.readUntil(agent, msgEnded)
	if h.srv.agentOnCall(h.tenant.String(), "alice") {
		t.Fatal("agent still busy")
	}
}

func TestAgentCampaignUnansweredAgentNeverDialsClient(t *testing.T) {
	h, m := agentCampaignHarness(t, []string{"alice"})
	startAgentCampaign(h, m)
	h.readUntil(h.pres, msgIncoming)
	if got := campaignResult(t, m); got != "no_answer:agent_not_answered" {
		t.Fatal(got)
	}
	if len(h.o.dialed) != 0 {
		t.Fatal("client dialed without an agent")
	}
	if m.answered {
		t.Fatal("unanswered agent marked client answered")
	}
}

func TestAgentCampaignExcludesOtherTeamsAndBusyAgents(t *testing.T) {
	for _, mode := range []string{"other_team", "busy", "away"} {
		t.Run(mode, func(t *testing.T) {
			h, m := agentCampaignHarness(t, []string{"alice"})
			h.srv.mu.Lock()
			switch mode {
			case "busy":
				h.srv.onCall[h.tenant.String()+"|alice"] = 1
			case "away":
				h.srv.away[h.tenant.String()+"|alice"] = true
			case "other_team":
				teams := shortTeams{members: map[string][]string{"sales": {"bob"}}}
				h.srv.cfg.Teams = teams
				h.srv.agentRouter = brain.NewAgentRouter(teams, h.srv.router, h.srv, nil, h.srv.log)
			}
			h.srv.mu.Unlock()
			startAgentCampaign(h, m)
			if got := campaignResult(t, m); got != "no_answer:agent_not_answered" {
				t.Fatal(got)
			}
			if len(h.o.dialed) != 0 {
				t.Fatal("client dialed without an available team agent")
			}
		})
	}
}

func TestAgentCampaignAgentHangupCancelsClientRing(t *testing.T) {
	h, m := agentCampaignHarness(t, []string{"alice"}, connectLeg{})
	h.o.block[0] = true
	startAgentCampaign(h, m)
	agent := answerCampaign(t, h)
	<-h.o.dialed
	h.send(agent, msg{Type: msgBye})
	if got := campaignResult(t, m); got != "failed:agent_hangup" {
		t.Fatal(got)
	}
	if m.answered {
		t.Fatal("client ring marked answered")
	}
}

func TestAgentCampaignClientBusyReleasesAgent(t *testing.T) {
	h, m := agentCampaignHarness(t, []string{"alice"}, connectLeg{err: &room.DialFailure{Outcome: "busy", Err: context.DeadlineExceeded}})
	startAgentCampaign(h, m)
	agent := answerCampaign(t, h)
	if got := campaignResult(t, m); got != "busy:busy" {
		t.Fatal(got)
	}
	h.readUntil(agent, msgEnded)
	if h.srv.agentOnCall(h.tenant.String(), "alice") {
		t.Fatal("agent still busy after client busy")
	}
	if m.answered {
		t.Fatal("busy client marked answered")
	}
}

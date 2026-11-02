//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// TestAgentGetsAudioThroughGateway: a carrier call into a DID whose flow
// transfers to the agent reaches the pion agent over the real gateway, and the
// agent hears the customer's tone.
func TestAgentGetsAudioThroughGateway(t *testing.T) {
	api := requireStack(t)
	sip := uniq("ag-a1")
	agentID := api.CreateAgent(sip, "Agent Test")
	flow := api.CreateFlow(uniq("ag-flow"), TransferFlow(map[string]string{"agent_sip": sip}))
	did := "918" + runID + "1"
	api.CreateNumber(did, "inbound", "", flow)

	agent := NewAgent(t, api, agentID, sip, FreqAgent1)
	carrier := TheCarrier(t)
	customer := carrier.Dial(env("TRING_E2E_FS_EXTERNAL", ""), did, FreqCustomer)
	defer customer.Hangup()

	room, _, auto := agent.WaitIncoming(15 * time.Second)
	if auto {
		t.Errorf("inbound ring has auto=true; want a ring the agent accepts")
	}
	if err := agent.Join(room); err != nil {
		t.Fatalf("join: %v", err)
	}
	got := waitArrivals(t, agent.Got(), agent.JoinedAt().Add(500*time.Millisecond), 2*time.Second)
	if err := CheckArrives(got, 50, 2*time.Second); err != nil {
		t.Errorf("agent: %v", err)
	}
	if f, ok := Hears(got, 48000, Freqs); !ok || f != FreqCustomer {
		t.Errorf("agent hears %v (clear tone: %v), want the customer's %v Hz", f, ok, FreqCustomer)
	}
}

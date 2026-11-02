//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fixture is the tenant every call scenario shares, seeded once per run: two
// agents, a team of the second, a trunk to the fake carrier with an outbound
// number, an inbound DID transferring to the first agent, and the 880 Hz tone
// as the tenant's hold music.
type fixture struct {
	api           *API
	a1, a2        string // agent ids
	a1Sip, a2Sip  string
	team          string
	did, callerID string
}

var (
	seedOnce sync.Once
	seeded   *fixture
)

const (
	customerNumber   = "9800000001" // dialled as +919800000001
	connectAgentNum  = "9800000002"
	ruriCustomer     = "+91" + customerNumber
	ruriConnectAgent = "+91" + connectAgentNum
)

func seed(t *testing.T) *fixture {
	t.Helper()
	api := requireStack(t)
	seedOnce.Do(func() {
		f := &fixture{api: api, a1Sip: uniq("a1"), a2Sip: uniq("a2"), team: uniq("t1")}
		f.a1 = api.CreateAgent(f.a1Sip, "Agent One")
		f.a2 = api.CreateAgent(f.a2Sip, "Agent Two")
		api.CreateTeam(f.team)
		api.AddTeamAgent(f.team, f.a2)
		var phase float64
		api.SetHoldMusic(api.UploadAudio(uniq("hold"), WAV(Tone(FreqHold, 8000, 3*8000, &phase), 8000)))
		trunk := api.CreateTrunk(uniq("carrier"), carrierIP(t)+":5999")
		api.WaitTrunkUsable(trunk)
		f.callerID = "+917" + pad(runID, 9)
		api.DeactivateOutboundExcept(api.CreateNumber(f.callerID, "outbound", trunk, ""))
		f.did = "918" + runID + "2"
		api.CreateNumber(f.did, "inbound", "", api.CreateFlow(uniq("to-a1"), TransferFlow(map[string]string{"agent_sip": f.a1Sip})))
		seeded = f
	})
	if seeded == nil {
		t.Fatal("seeding failed in an earlier test")
	}
	seeded.api = api // this test's client (its t)
	return seeded
}

func pad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

// live runs a scenario up to 3 times while the agent's media never connects
// (in-process ICE stall); any other failure fails at once. Each attempt is its
// own subtest, so what it set up (agents, carrier legs, calls) is torn down
// before the next attempt starts.
func live(t *testing.T, scenario func(t *testing.T) error) {
	t.Helper()
	for i := 1; i <= 3; i++ {
		var err error
		t.Run(fmt.Sprintf("attempt-%d", i), func(t *testing.T) { err = scenario(t) })
		switch {
		case t.Failed():
			return
		case err == nil:
			return
		case !errors.Is(err, errNotLive):
			t.Fatal(err)
		}
		t.Logf("attempt %d: %v; retrying", i, err)
	}
	t.Fatal("agent media never connected in 3 attempts")
}

// checkHearsCustomer is the core check on what an agent's side got after the
// customer answered: audio arrives, on the right clock (step 0 skips the clock
// check), with no stale burst, and it is the customer.
func checkHearsCustomer(t *testing.T, who string, got []Arrival, rate int, step uint32) {
	t.Helper()
	main := Main(got)
	errs := []error{CheckArrives(main, 50, 2*time.Second), CheckNoBurst(main, 200*time.Millisecond, 15)}
	if step != 0 {
		errs = append(errs, CheckClock(main, step))
	}
	for _, err := range errs {
		if err != nil {
			t.Errorf("%s: %v", who, err)
		}
	}
	if f, ok := Hears(got, rate, Freqs); !ok || f != FreqCustomer {
		t.Errorf("%s hears %v (clear tone: %v), want the customer's %v Hz", who, f, ok, FreqCustomer)
	}
}

func TestInboundAgentHearsCustomer(t *testing.T) {
	live(t, func(t *testing.T) error {
		f := seed(t) // this attempt's client
		a1 := NewAgent(t, f.api, f.a1, f.a1Sip, FreqAgent1)
		customer := TheCarrier(t).Dial(env("TRING_E2E_FS_EXTERNAL", ""), f.did, FreqCustomer)
		defer customer.Hangup()
		room, _, _ := a1.WaitIncoming(15 * time.Second)
		if err := a1.Join(room); err != nil {
			return err
		}
		from := a1.JoinedAt().Add(500 * time.Millisecond)
		checkHearsCustomer(t, "agent", waitArrivals(t, a1.Got(), from, 2*time.Second), opusRate, opusFrame)
		if fq, ok := Hears(Between(customer.Got.Since(from), from, from.Add(2*time.Second)), 8000, Freqs); !ok || fq != FreqAgent1 {
			t.Errorf("customer hears %v (clear tone: %v), want agent one's %v Hz", fq, ok, FreqAgent1)
		}
		return nil
	})
}

func TestClickToCallAgentHearsCustomer(t *testing.T) {
	live(t, func(t *testing.T) error {
		f := seed(t) // this attempt's client
		a1 := NewAgent(t, f.api, f.a1, f.a1Sip, FreqAgent1)
		carrier := TheCarrier(t)
		customer := carrier.OnNumber(ruriCustomer, FreqCustomer)
		defer customer.Hangup()
		f.api.Call(f.a1Sip, customerNumber)
		room, _, auto := a1.WaitIncoming(15 * time.Second)
		if !auto {
			t.Errorf("click-to-call ring has auto=false; the widget answers it itself")
		}
		if err := a1.Join(room); err != nil {
			return err
		}
		a1.WaitMsg("answered", 20*time.Second)
		checkHearsCustomer(t, "agent", waitArrivals(t, a1.Got(), customer.AnsweredAt(), 2*time.Second), opusRate, opusFrame)
		return nil
	})
}

func TestConnectAgentHearsCustomer(t *testing.T) {
	f := seed(t)
	carrier := TheCarrier(t)
	agentPhone := carrier.OnNumber(ruriConnectAgent, FreqAgent1)
	customer := carrier.OnNumber(ruriCustomer, FreqCustomer)
	defer agentPhone.Hangup()
	defer customer.Hangup()
	f.api.Connect(connectAgentNum, customerNumber)
	select {
	case <-customer.Answered:
	case <-time.After(30 * time.Second):
		t.Fatal("the customer's phone was never answered (dialled after the agent's)")
	}
	// No clock check here. FreeSWITCH sends the agent's phone G.711 stamped 960
	// per 20 ms (not 160) with the marker bit on every packet: it carries the
	// gateway's 48 kHz stamps through a leg it runs at 8 kHz since #135. Live
	// calls work both ways on the carrier, so this is accepted (decision
	// 2026-11-02); a check that fails here would only re-find it.
	checkHearsCustomer(t, "agent's phone", waitArrivals(t, agentPhone.Got, customer.AnsweredAt(), 2*time.Second), 8000, 0)
}

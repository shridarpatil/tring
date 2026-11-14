//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// settle is how long after a hold, resume or join the media is given to
// switch before a window is measured.
const settle = 700 * time.Millisecond

// call is a connected call between agent one (a browser agent) and a customer.
type call struct {
	a1       *Agent
	customer *Leg
}

// inboundCall connects a carrier call through the DID's flow to agent one.
func inboundCall(t *testing.T, f *fixture) (call, error) {
	a1 := NewAgent(t, f.api, f.a1, f.a1Sip, FreqAgent1)
	customer := TheCarrier(t).Dial(env("TRING_E2E_FS_EXTERNAL", ""), f.did, FreqCustomer)
	t.Cleanup(customer.Hangup)
	room, _, _ := a1.WaitIncoming(15 * time.Second)
	return call{a1, customer}, a1.Join(room)
}

// clickToCall connects a POST /v1/calls from agent one to the customer.
func clickToCall(t *testing.T, f *fixture) (call, error) {
	a1 := NewAgent(t, f.api, f.a1, f.a1Sip, FreqAgent1)
	customer := TheCarrier(t).OnNumber(ruriCustomer, FreqCustomer)
	t.Cleanup(customer.Hangup)
	f.api.Call(f.a1Sip, customerNumber)
	room, _, _ := a1.WaitIncoming(15 * time.Second)
	if err := a1.Join(room); err != nil {
		return call{}, err
	}
	a1.WaitMsg("answered", 20*time.Second)
	return call{a1, customer}, nil
}

// hears asserts what a party heard in the window [from, from+2s).
func hears(t *testing.T, who string, r *Recorder, rate int, from time.Time, want float64) {
	t.Helper()
	f, ok := Hears(waitArrivals(t, r, from, 2*time.Second), rate, Freqs)
	switch {
	case want == 0 && ok:
		t.Errorf("%s hears %v Hz, want nothing", who, f)
	case want != 0 && (!ok || f != want):
		t.Errorf("%s hears %v (clear tone: %v), want %v Hz", who, f, ok, want)
	}
}

func TestHoldResume(t *testing.T) {
	for name, place := range map[string]func(*testing.T, *fixture) (call, error){
		"inbound":       inboundCall,
		"click-to-call": clickToCall,
	} {
		t.Run(name, func(t *testing.T) {
			live(t, func(t *testing.T) error {
				f := seed(t) // this attempt's client
				c, err := place(t, f)
				if err != nil {
					return err
				}
				// Held: the customer gets the music, even though the agent keeps
				// sending (silent) frames, and the agent hears nothing.
				c.a1.Hold()
				from := time.Now().Add(settle)
				hears(t, "held customer", c.customer.Got, 8000, from, FreqHold)
				hears(t, "holding agent", c.a1.Got(), opusRate, from, 0)

				c.a1.Resume()
				from = time.Now().Add(settle)
				hears(t, "resumed customer", c.customer.Got, 8000, from, FreqAgent1)
				hears(t, "resumed agent", c.a1.Got(), opusRate, from, FreqCustomer)
				return nil
			})
		})
	}
}

func TestTransferToAgent(t *testing.T) {
	testTransfer(t, func(f *fixture, a1 *Agent) { a1.TransferToAgent(f.a2Sip) })
}

func TestTransferToTeam(t *testing.T) {
	testTransfer(t, func(f *fixture, a1 *Agent) { a1.TransferToTeam(f.team) })
}

// testTransfer: agent one transfers an inbound call and stays on it while
// agent two (the team's only member) is rung. Agent two joins and then hears
// the customer and is heard by them; only then does agent one's leg end.
func testTransfer(t *testing.T, transfer func(*fixture, *Agent)) {
	live(t, func(t *testing.T) error {
		f := seed(t) // this attempt's client
		a2 := NewAgent(t, f.api, f.a2, f.a2Sip, FreqAgent2)
		c, err := inboundCall(t, f)
		if err != nil {
			return err
		}
		transfer(f, c.a1)
		c.a1.WaitMsg("transfer-ok", 10*time.Second)
		room, _, _ := a2.WaitIncoming(15 * time.Second)
		// Transfer with fallback: agent one waits on the call until someone
		// answers, so nothing may have ended its leg yet.
		for drained := false; !drained; {
			select {
			case m := <-c.a1.ctrl:
				if m.Type == "ended" {
					t.Errorf("agent one's leg ended before agent two answered")
				}
			default:
				drained = true
			}
		}
		if err := a2.Join(room); err != nil {
			return err
		}
		c.a1.WaitMsg("ended", 10*time.Second)
		from := a2.JoinedAt().Add(settle)
		hears(t, "transferred customer", c.customer.Got, 8000, from, FreqAgent2)
		got := Between(a2.Got().Since(from), from, from.Add(2*time.Second))
		if f, ok := Hears(got, opusRate, Freqs); !ok || f != FreqCustomer {
			t.Errorf("agent two hears %v (clear tone: %v), want the customer's %v Hz", f, ok, FreqCustomer)
		}
		if err := CheckClock(Main(got), opusFrame); err != nil {
			t.Errorf("agent two: %v", err)
		}
		return nil
	})
}

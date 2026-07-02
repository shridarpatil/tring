package sfu_test

// End-to-end verification of the supervisor audience gating (Tasks 1-2) at the
// MEDIA level: three real pion peers — customer, agent, supervisor — join one
// room over actual PeerConnections, and we assert WHO receives the supervisor's
// RTP under each mode:
//
//	monitor — nobody hears the supervisor (customer AND agent get no sup audio)
//	whisper — only the agent hears the supervisor (customer does not)
//	barge   — everyone hears the supervisor
//
// In every mode the supervisor still hears both parties. Because in-process
// pion ICE/DTLS over a single host is racy (see sfu_test.go), each scenario is
// retried; a PASS requires, IN THE SAME attempt, that all the positive liveness
// flows arrived (proving the peers really connected) AND the gated flow was
// absent — so a dead connection can never masquerade as correct gating.

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// rtpCounter records, per received stream id (= the publisher's ParticipantID),
// how many RTP packets a peer got — so we can tell whose audio reached whom.
type rtpCounter struct {
	mu    sync.Mutex
	byPID map[string]int
}

func newRTPCounter() *rtpCounter { return &rtpCounter{byPID: map[string]int{}} }

func (r *rtpCounter) record(streamID string) {
	r.mu.Lock()
	r.byPID[streamID]++
	r.mu.Unlock()
}

func (r *rtpCounter) count(pid string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byPID[pid]
}

// countStream drains a received track, tallying packets under its stream id.
func countStream(c *rtpCounter, tr *webrtc.TrackRemote) {
	for {
		if _, _, err := tr.ReadRTP(); err != nil {
			return
		}
		c.record(tr.StreamID())
	}
}

// superviseAudienceOnce runs one attempt of a 3-peer scenario for the given
// supervisor audience and returns whether the expected routing held. all/allowed
// are passed straight to SFU.SetAudience for the supervisor. wantSupAtCustomer /
// wantSupAtAgent say whether the customer / agent should end up hearing the
// supervisor. The supervisor must always hear both other parties.
func superviseAudienceOnce(t *testing.T, all bool, allowed []room.ParticipantID, wantSupAtCustomer, wantSupAtAgent bool) bool {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()

	const rid = room.RoomID("call")
	customer := newClient(t, s, clientAPI(t), rid, "customer", &closers)
	agent := newClient(t, s, clientAPI(t), rid, "agent", &closers)
	supervisor := newClient(t, s, clientAPI(t), rid, "supervisor", &closers)

	custGot, agentGot, supGot := newRTPCounter(), newRTPCounter(), newRTPCounter()
	customer.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { countStream(custGot, tr) })
	agent.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { countStream(agentGot, tr) })
	supervisor.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { countStream(supGot, tr) })

	// Pin the supervisor's audience BEFORE anyone publishes/connects, so no
	// mid-call renegotiation is needed — the room exists now (the joins above
	// created it), and publicationsExcept honors the audience at subscribe time.
	s.SetAudience(rid, "supervisor", all, allowed)

	customer.publishOpus(t, &closers)
	agent.publishOpus(t, &closers)
	supervisor.publishOpus(t, &closers)
	customer.connect()
	agent.connect()
	supervisor.connect()

	// Positive liveness flows that MUST arrive for this attempt to count: the
	// customer<->agent pair always hear each other, and the supervisor always
	// hears both. Only once these hold do we trust a negative (absence) check.
	const threshold = 15
	deadline := time.Now().Add(6 * time.Second)
	live := func() bool {
		return custGot.count("agent") >= threshold &&
			agentGot.count("customer") >= threshold &&
			supGot.count("customer") >= threshold &&
			supGot.count("agent") >= threshold
	}
	for time.Now().Before(deadline) {
		if live() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !live() {
		return false // in-process ICE stall — retry the whole scenario
	}

	// Liveness holds. Give any (wrongly) gated supervisor audio a fair chance to
	// show up before asserting its absence, so we're not just racing it.
	if !wantSupAtCustomer || !wantSupAtAgent {
		time.Sleep(500 * time.Millisecond)
	}

	gotSupAtCustomer := custGot.count("supervisor") >= threshold
	gotSupAtAgent := agentGot.count("supervisor") >= threshold

	// For a party that SHOULD hear the supervisor, require real flow; for one
	// that should NOT, require exactly zero packets (not just below threshold).
	if wantSupAtCustomer {
		if !gotSupAtCustomer {
			return false // should hear sup but didn't — could be an ICE stall on that leg
		}
	} else if custGot.count("supervisor") != 0 {
		t.Fatalf("LEAK: customer heard the supervisor (%d pkts) when it must not (all=%v allowed=%v)",
			custGot.count("supervisor"), all, allowed)
	}
	if wantSupAtAgent {
		if !gotSupAtAgent {
			return false
		}
	} else if agentGot.count("supervisor") != 0 {
		t.Fatalf("LEAK: agent heard the supervisor (%d pkts) when it must not (all=%v allowed=%v)",
			agentGot.count("supervisor"), all, allowed)
	}
	return true
}

// runSupervise retries a scenario until the expected routing is observed on a
// live attempt, or fails after exhausting attempts. A genuine gating LEAK fails
// fast (t.Fatalf inside superviseAudienceOnce); retries only absorb ICE stalls.
func runSupervise(t *testing.T, name string, all bool, allowed []room.ParticipantID, wantCust, wantAgent bool) {
	t.Helper()
	const attempts = 10
	for i := 1; i <= attempts; i++ {
		if superviseAudienceOnce(t, all, allowed, wantCust, wantAgent) {
			return
		}
		t.Logf("%s: attempt %d/%d did not reach a live steady state, retrying", name, i, attempts)
	}
	t.Fatalf("%s: expected routing never observed on a live attempt after %d tries", name, attempts)
}

func TestSupervisorMonitorE2E(t *testing.T) {
	// Monitor: audience = nobody. Neither customer nor agent may hear the sup.
	runSupervise(t, "monitor", false, nil, false, false)
}

func TestSupervisorWhisperE2E(t *testing.T) {
	// Whisper: audience = the agent only. Customer must NOT hear the sup.
	runSupervise(t, "whisper", false, []room.ParticipantID{"agent"}, false, true)
}

func TestSupervisorBargeE2E(t *testing.T) {
	// Barge: audience = everyone. Both customer and agent hear the sup.
	runSupervise(t, "barge", true, nil, true, true)
}

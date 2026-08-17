package sfu_test

// End-to-end verification of hold at the MEDIA level: two real pion peers — a
// customer and an agent — are in one room with audio flowing both ways, then
// the agent is deafened (what msgHold does server-side) and hold music is
// injected into the room. After that the agent must receive NOTHING — neither
// the customer's audio nor the hold music — while the customer keeps hearing
// the agent and starts hearing the music.
//
// Like the supervisor audience e2e, in-process pion ICE is racy, so each
// attempt is retried; a PASS requires, IN THE SAME attempt, that the positive
// flows were live AND the gated flows were absent — a dead connection can never
// masquerade as correct gating.

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// pacedSource is a room.ParticipantSource emitting one Opus packet every 20ms
// until closed — a stand-in for the hold-music player, which is injected into
// the room exactly this way.
type pacedSource struct {
	id   room.ParticipantID
	seq  uint16
	done chan struct{}
	once sync.Once
}

func newPacedSource(id room.ParticipantID) *pacedSource {
	return &pacedSource{id: id, done: make(chan struct{})}
}

func (p *pacedSource) ID() room.ParticipantID { return p.id }
func (p *pacedSource) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *pacedSource) ReadRTP() (*rtp.Packet, error) {
	select {
	case <-p.done:
		return nil, io.EOF
	case <-time.After(20 * time.Millisecond):
	}
	p.seq++
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, SequenceNumber: p.seq,
			Timestamp: uint32(p.seq) * 960, PayloadType: 111,
		},
		Payload: []byte{0xfc, 0xff, 0xfe},
	}, nil
}

// holdDeafensAgentOnce runs one attempt. Returns false if the peers never
// reached a live steady state (retry); fails the test outright on a media leak.
func holdDeafensAgentOnce(t *testing.T) bool {
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

	custGot, agentGot := newRTPCounter(), newRTPCounter()
	customer.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { countStream(custGot, tr) })
	agent.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { countStream(agentGot, tr) })

	customer.publishOpus(t, &closers)
	agent.publishOpus(t, &closers)
	customer.connect()
	agent.connect()

	// Both directions must be live before hold means anything.
	const threshold = 15
	deadline := time.Now().Add(6 * time.Second)
	live := func() bool {
		return custGot.count("agent") >= threshold && agentGot.count("customer") >= threshold
	}
	for time.Now().Before(deadline) && !live() {
		time.Sleep(50 * time.Millisecond)
	}
	if !live() {
		return false // in-process ICE stall — retry the whole scenario
	}

	// Hold: deafen the holding agent FIRST (so no music frame can reach it),
	// then inject the hold music as an ordinary room publication.
	s.SetDeaf(rid, "agent", true)
	music := newPacedSource("hold-music")
	defer func() { _ = music.Close() }()
	if err := s.InjectSource(rid, music, webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}); err != nil {
		t.Fatalf("inject hold music: %v", err)
	}

	// Settle: let the agent's renegotiation land and in-flight packets drain,
	// then take the baseline the absence check is measured against.
	time.Sleep(700 * time.Millisecond)
	baseCustAtAgent := agentGot.count("customer")
	baseMusicAtAgent := agentGot.count("hold-music")
	baseAgentAtCust := custGot.count("agent")
	time.Sleep(800 * time.Millisecond)

	if got := agentGot.count("customer"); got > baseCustAtAgent {
		t.Fatalf("LEAK: held agent still hears the customer (%d new pkts)", got-baseCustAtAgent)
	}
	if got := agentGot.count("hold-music"); got > baseMusicAtAgent {
		t.Fatalf("LEAK: held agent hears the hold music (%d pkts)", got-baseMusicAtAgent)
	}
	// Positive flows: the customer keeps hearing the agent (deafening is
	// subscriber-side only) and now hears the music.
	if custGot.count("agent") <= baseAgentAtCust {
		return false // customer's inbound stalled — retry
	}
	if custGot.count("hold-music") < threshold {
		return false // music never reached the customer — retry
	}
	return true
}

func TestHoldDeafensAgentE2E(t *testing.T) {
	const attempts = 10
	for i := 1; i <= attempts; i++ {
		if holdDeafensAgentOnce(t) {
			return
		}
		t.Logf("hold: attempt %d/%d did not reach a live steady state, retrying", i, attempts)
	}
	t.Fatalf("hold routing never observed on a live attempt after %d tries", attempts)
}

package sfu_test

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

func opusCap() webrtc.RTPCodecCapability {
	return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
}

// pktCollector records the RTP a port writes toward its external leg.
type pktCollector struct {
	mu   sync.Mutex
	pkts []*rtp.Packet
}

func (c *pktCollector) write(p *rtp.Packet) error {
	c.mu.Lock()
	c.pkts = append(c.pkts, p)
	c.mu.Unlock()
	return nil
}

func (c *pktCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pkts)
}

func (c *pktCollector) payloads() map[byte]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[byte]bool{}
	for _, p := range c.pkts {
		if len(p.Payload) > 0 {
			seen[p.Payload[0]] = true
		}
	}
	return seen
}

// slowSource is a fakeSource that paces its packets so sequencing is observable.
type slowSource struct {
	id      room.ParticipantID
	mark    byte // first payload byte, identifies this source in the collector
	total   int
	sent    int
	mu      sync.Mutex
	stopped bool
}

func (f *slowSource) ID() room.ParticipantID { return f.id }
func (f *slowSource) Close() error {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	return nil
}
func (f *slowSource) ReadRTP() (*rtp.Packet, error) {
	f.mu.Lock()
	if f.stopped || f.sent >= f.total {
		f.mu.Unlock()
		return nil, errEOF
	}
	f.sent++
	seq := uint16(f.sent)
	f.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, PayloadType: 111},
		Payload: []byte{f.mark, 0x01},
	}, nil
}

var errEOF = errClosedSentinel{}

type errClosedSentinel struct{}

func (errClosedSentinel) Error() string { return "source drained" }

// TestPortHearsRoomAndSwitchesFeeds is the 4a contract: a phone-side port hears
// the room's active publisher (the IVR voice), and when that publisher leaves,
// the port switches to the next one (the agent) — one continuous RTP stream.
func TestPortHearsRoomAndSwitchesFeeds(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}

	// Attach the phone port (caller's leg): publishes into the room + hears it.
	caller := &slowSource{id: "phone-leg", mark: 0xCC, total: 200}
	out := &pktCollector{}
	if err := s.AttachPort("call", caller, out.write); err != nil {
		t.Fatalf("attach port: %v", err)
	}

	// IVR voice joins (injected source) — the port must hear it.
	ivr := &slowSource{id: "ivr", mark: 0xAA, total: 50}
	if err := s.InjectSource("call", ivr, opusCap()); err != nil {
		t.Fatalf("inject ivr: %v", err)
	}
	waitCount(t, out, 10, "port hears the IVR")
	if !out.payloads()[0xAA] {
		t.Fatal("port did not receive IVR-marked packets")
	}

	// IVR leaves; the agent joins (another source stands in for the agent's
	// publication). The port must switch to the agent's feed.
	s.RemoveSource("call", "ivr")
	agent := &slowSource{id: "agent", mark: 0xBB, total: 200}
	if err := s.InjectSource("call", agent, opusCap()); err != nil {
		t.Fatalf("inject agent: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !out.payloads()[0xBB] {
		time.Sleep(10 * time.Millisecond)
	}
	if !out.payloads()[0xBB] {
		t.Fatal("port did not switch to the agent's feed after the IVR left")
	}

	// And the port must never echo its own (caller) packets back out.
	if out.payloads()[0xCC] {
		t.Fatal("port echoed the caller's own audio back to the leg")
	}

	// The outbound stream is continuous: one SSRC, strictly increasing seq.
	out.mu.Lock()
	defer out.mu.Unlock()
	var lastSeq uint16
	for i, p := range out.pkts {
		if p.SSRC != out.pkts[0].SSRC {
			t.Fatal("outbound SSRC changed across feed switchover")
		}
		if i > 0 && p.SequenceNumber != lastSeq+1 {
			t.Fatalf("seq not continuous at %d: %d → %d", i, lastSeq, p.SequenceNumber)
		}
		lastSeq = p.SequenceNumber
	}
}

func waitCount(t *testing.T, c *pktCollector, n int, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s (got %d packets)", msg, c.count())
}

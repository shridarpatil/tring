package sfu_test

import (
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// silentSignaler drops the SFU's offer and candidates: a client that joins and
// never answers, holding a peer (and, without the mux, a port) open.
type silentSignaler struct{}

func (silentSignaler) SendOffer(string) error                      { return nil }
func (silentSignaler) SendCandidate(webrtc.ICECandidateInit) error { return nil }

// TestNeverConnectedPeerIsClosed: a peer that doesn't connect within
// ConnectTimeout is closed, firing OnClosed so the room tears the slot down.
func TestNeverConnectedPeerIsClosed(t *testing.T) {
	s, err := sfu.New(sfu.Config{ConnectTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	p, err := s.Join("call", "idle", silentSignaler{})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	closed := make(chan struct{}, 1)
	p.OnClosed = func() { closed <- struct{}{} }
	p.Negotiate()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("never-connected peer was not closed after ConnectTimeout")
	}
}

// TestForwardsAudioOverUDPMux: with the mux on, every peer's host candidate is
// the one shared port and media still flows between participants.
func TestForwardsAudioOverUDPMux(t *testing.T) {
	const attempts = 8
	for i := 1; i <= attempts; i++ {
		if forwardOnceCfg(t, sfu.Config{UDPMux: true, UDPPortMin: freeUDPPort(t), IncludeLoopback: true}) {
			return
		}
		t.Logf("attempt %d/%d: media did not flow (in-process ICE stall), retrying", i, attempts)
	}
	t.Fatalf("B never received A's forwarded audio over the UDP mux after %d attempts", attempts)
}

// TestUDPMuxNeedsAPort: the mux binds UDPPortMin, so it can't be enabled
// without one.
func TestUDPMuxNeedsAPort(t *testing.T) {
	if _, err := sfu.New(sfu.Config{UDPMux: true}); err == nil {
		t.Fatal("sfu.New accepted UDPMux with no port")
	}
}

func freeUDPPort(t *testing.T) uint16 {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer c.Close()
	return uint16(c.LocalAddr().(*net.UDPAddr).Port)
}

// candSignaler records the SFU's ICE candidates.
type candSignaler struct{ cands chan webrtc.ICECandidateInit }

func (candSignaler) SendOffer(string) error { return nil }
func (c candSignaler) SendCandidate(ci webrtc.ICECandidateInit) error {
	c.cands <- ci
	return nil
}

// TestUDPMuxSharesOnePort: two peers' host candidates both sit on the one mux
// port, rather than each peer binding its own.
func TestUDPMuxSharesOnePort(t *testing.T) {
	port := freeUDPPort(t)
	s, err := sfu.New(sfu.Config{UDPMux: true, UDPPortMin: port, IncludeLoopback: true})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	for _, pid := range []string{"a", "b"} {
		sig := candSignaler{cands: make(chan webrtc.ICECandidateInit, 16)}
		p, err := s.Join("call", room.ParticipantID(pid), sig)
		if err != nil {
			t.Fatalf("join %s: %v", pid, err)
		}
		p.Negotiate()
		select {
		case c := <-sig.cands:
			cand, err := ice.UnmarshalCandidate(c.Candidate)
			if err != nil {
				t.Fatalf("parse candidate %q: %v", c.Candidate, err)
			}
			if cand.Port() != int(port) {
				t.Fatalf("peer %s host candidate port = %d, want mux port %d", pid, cand.Port(), port)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("peer %s gathered no candidate", pid)
		}
	}
}

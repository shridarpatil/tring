package sfu

import (
	"log/slog"
	"testing"

	"github.com/pion/webrtc/v4"
)

// Candidates trickled before the first answer are queued — but only up to
// maxPendingCandidates, so a client can't grow the queue without bound.
func TestPendingCandidatesCapped(t *testing.T) {
	p := &Peer{log: slog.Default()}
	for i := 0; i < maxPendingCandidates*3; i++ {
		if err := p.HandleCandidate(webrtc.ICECandidateInit{Candidate: "candidate:1 1 udp 1 192.0.2.1 9 typ host"}); err != nil {
			t.Fatalf("HandleCandidate: %v", err)
		}
	}
	if len(p.pending) != maxPendingCandidates {
		t.Fatalf("pending = %d, want %d", len(p.pending), maxPendingCandidates)
	}
}

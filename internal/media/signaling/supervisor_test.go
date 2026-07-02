package signaling

import (
	"testing"

	"github.com/shridarpatil/tring/internal/media/room"
)

func TestSuperviseModeAudience(t *testing.T) {
	agents := []room.ParticipantID{"a1", "a2"}

	all, allowed := superviseAudience("monitor", agents)
	if all || len(allowed) != 0 {
		t.Fatalf("monitor: got all=%v allowed=%v", all, allowed)
	}
	all, allowed = superviseAudience("whisper", agents)
	if all || len(allowed) != 2 {
		t.Fatalf("whisper: got all=%v allowed=%v", all, allowed)
	}
	all, allowed = superviseAudience("barge", agents)
	if !all {
		t.Fatalf("barge: got all=%v", all)
	}
	// Unknown mode falls back to monitor (safest — supervisor stays silent).
	all, allowed = superviseAudience("bogus", agents)
	if all || len(allowed) != 0 {
		t.Fatalf("bogus must fall back to monitor")
	}
}

func TestValidSuperviseMode(t *testing.T) {
	for _, m := range []string{"monitor", "whisper", "barge"} {
		if !validSuperviseMode(m) {
			t.Fatalf("%q should be valid", m)
		}
	}
	if validSuperviseMode("barge-in") {
		t.Fatal("unknown mode must be invalid")
	}
}

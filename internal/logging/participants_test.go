package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// participantID mimics room.ParticipantID: a named string type.
type participantID string

func TestParticipantTagsAdded(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	SetParticipant("p-agent", slog.String("role", "agent"), slog.String("sip", "alice"))
	defer ClearParticipant("p-agent")

	log.Info("participant joined", "room", "t1/x", "participant", participantID("p-agent"))
	out := buf.String()
	if !strings.Contains(out, "role=agent") || !strings.Contains(out, "sip=alice") {
		t.Fatalf("want role=agent sip=alice in %q", out)
	}
}

func TestParticipantTagsFromWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	SetParticipant("sup-1", slog.String("role", "supervisor"), slog.String("user", "Priya S"))
	defer ClearParticipant("sup-1")
	log := newTestLogger(&buf).With("room", "t1/x", "participant", participantID("sup-1"))

	log.Info("ice state")
	if out := buf.String(); !strings.Contains(out, "role=supervisor") || !strings.Contains(out, `user="Priya S"`) {
		t.Fatalf("want role=supervisor user=\"Priya S\" in %q", out)
	}
}

func TestParticipantTagsNotDuplicated(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	SetParticipant("p-x", slog.String("role", "caller"))
	defer ClearParticipant("p-x")

	log.Info("joined", "participant", "p-x", "role", "caller")
	if n := strings.Count(buf.String(), "role="); n != 1 {
		t.Fatalf("want one role= attr, got %d in %q", n, buf.String())
	}
}

func TestParticipantCleared(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	SetParticipant("p-gone", slog.String("role", "callee"))
	ClearParticipant("p-gone")

	log.Info("participant left", "participant", "p-gone")
	if strings.Contains(buf.String(), "role=") {
		t.Fatalf("unexpected role= after clear in %q", buf.String())
	}
}

func TestCallAndParticipantTogether(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	SetCall("t1/pstn-z", "call-9")
	defer ClearCall("t1/pstn-z", "call-9")
	SetParticipant("p-caller", slog.String("role", "caller"))
	defer ClearParticipant("p-caller")

	log.Info("source injected", "room", "t1/pstn-z", "participant", "p-caller")
	if out := buf.String(); !strings.Contains(out, "call=call-9") || !strings.Contains(out, "role=caller") {
		t.Fatalf("want call=call-9 and role=caller in %q", out)
	}
}

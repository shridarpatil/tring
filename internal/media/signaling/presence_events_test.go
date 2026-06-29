package signaling

import (
	"context"
	"log/slog"
	"testing"

	"github.com/shridarpatil/tring/internal/events"
)

type captureSink struct {
	types    []string
	statuses []string
}

func (c *captureSink) Publish(_ context.Context, _, typ string, data any) {
	c.types = append(c.types, typ)
	if m, ok := data.(map[string]any); ok {
		if s, ok := m["status"].(string); ok {
			c.statuses = append(c.statuses, s)
		}
	}
}

func TestSetAgentOnCallPublishesPresence(t *testing.T) {
	sink := &captureSink{}
	s := &Server{
		cfg:    Config{Events: sink},
		onCall: map[string]int{},
		log:    slog.Default(),
	}
	s.setAgentOnCall("tenant-1", "alice", true)
	s.setAgentOnCall("tenant-1", "alice", false)

	if len(sink.types) != 2 {
		t.Fatalf("got %d publishes, want 2: %+v", len(sink.types), sink.types)
	}
	for _, ty := range sink.types {
		if ty != events.TypeAgentPresence {
			t.Fatalf("unexpected type %q", ty)
		}
	}
	if sink.statuses[0] != "on_call" || sink.statuses[1] != "online" {
		t.Fatalf("statuses = %+v, want [on_call online]", sink.statuses)
	}
}

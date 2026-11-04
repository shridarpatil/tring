package signaling

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/presence"
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

// The longest_wait ring strategy orders agents by presence.FreedAt, so the
// moment an agent's last call leg ends must be stamped — and only then, not
// while a transfer overlap still holds a second leg.
func TestSetAgentOnCallMarksFreed(t *testing.T) {
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
	if err != nil || c == nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	pres := presence.New(c)
	ctx := context.Background()
	tenant := "test-" + uuid.NewString()

	s := &Server{
		cfg:    Config{Presence: pres},
		onCall: map[string]int{},
		log:    slog.Default(),
	}
	s.setAgentOnCall(tenant, "alice", true)
	s.setAgentOnCall(tenant, "alice", true) // transfer overlap: two legs
	s.setAgentOnCall(tenant, "alice", false)
	if _, ok := pres.FreedAt(ctx, tenant, "alice"); ok {
		t.Fatal("freed-at stamped while the agent still holds a call leg")
	}

	before := time.Now()
	s.setAgentOnCall(tenant, "alice", false)
	got, ok := pres.FreedAt(ctx, tenant, "alice")
	if !ok {
		t.Fatal("freed-at not stamped when the agent's last call ended")
	}
	if got.Before(before) {
		t.Fatalf("freed-at %v predates the call ending (%v)", got, before)
	}
}

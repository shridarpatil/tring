package signaling

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/presence"
)

type captureSink struct {
	mu        sync.Mutex
	types     []string
	statuses  []string
	idleSince []any // per publish; nil when the event carried no idle_since
}

func (c *captureSink) Publish(_ context.Context, _, typ string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.types = append(c.types, typ)
	if m, ok := data.(map[string]any); ok {
		if s, ok := m["status"].(string); ok {
			c.statuses = append(c.statuses, s)
		}
		c.idleSince = append(c.idleSince, m["idle_since"])
	}
}

// testPresence is a presence store on a real Redis (TRING_TEST_REDIS or the dev
// default), skipping when none is reachable.
func testPresence(t *testing.T) *presence.Store {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
	if err != nil || c == nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	return presence.New(c)
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
	pres := testPresence(t)
	ctx := context.Background()
	tenant := "test-" + uuid.NewString()
	sink := &captureSink{}

	s := &Server{
		cfg:    Config{Presence: pres, Events: sink},
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
	// The dashboard's idle timer starts from the online event's idle_since.
	last := sink.idleSince[len(sink.idleSince)-1]
	if ts, ok := last.(time.Time); !ok || !ts.Equal(got) {
		t.Fatalf("online event idle_since = %v, want %v", last, got)
	}
}

// Coming back from Away — or connecting as available, which flips the
// registered-as-Away placeholder — restarts the idle clock once. A repeated
// "available" from the backend must not reset it.
func TestBecomingAvailableMarksFreed(t *testing.T) {
	pres := testPresence(t)
	ctx := context.Background()
	tenant := "test-" + uuid.NewString()
	sink := &captureSink{}
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	c := &conn{Conn: serverConn}
	s := &Server{
		cfg:    Config{Presence: pres, Events: sink},
		agents: map[string]map[string]*conn{tenant: {"alice": c}},
		onCall: map[string]int{},
		log:    slog.Default(),
	}
	s.setAway(c, tenant, "alice", true)
	if _, ok := pres.FreedAt(ctx, tenant, "alice"); ok {
		t.Fatal("going Away stamped freed-at")
	}

	go s.PushAvailability(tenant, "alice", true)
	readMsg(t, clientConn)
	first, ok := pres.FreedAt(ctx, tenant, "alice")
	if !ok {
		t.Fatal("Away -> available did not stamp freed-at")
	}
	sink.mu.Lock()
	last := sink.idleSince[len(sink.idleSince)-1]
	sink.mu.Unlock()
	if ts, ok := last.(time.Time); !ok || !ts.Equal(first) {
		t.Fatalf("published idle_since = %v, want %v", last, first)
	}

	go s.PushAvailability(tenant, "alice", true) // already available
	readMsg(t, clientConn)
	if again, _ := pres.FreedAt(ctx, tenant, "alice"); !again.Equal(first) {
		t.Fatalf("repeated available reset freed-at: %v -> %v", first, again)
	}
}

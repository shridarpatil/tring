package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/presence"
)

// An available agent's freed-at stamp is surfaced as idle_since, which the
// dashboard turns into an "idle 4:12" timer. Busy, Away and offline agents
// carry none — idle only means connected, available and not on a call.
func TestAgentsListIdleSince(t *testing.T) {
	f := dbtest.Setup(t)
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
	if err != nil || c == nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	ctx := context.Background()
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", Cache: c, AgentTransport: "gateway"})
	tenant := f.TenantID.String()

	for _, q := range []string{
		`INSERT INTO agents (tenant_id, sip_username, sip_password) VALUES ($1, 'idle-a', 'x')`,
		`INSERT INTO agents (tenant_id, sip_username, sip_password) VALUES ($1, 'busy-b', 'x')`,
		`INSERT INTO agents (tenant_id, sip_username, sip_password, available_for_calls) VALUES ($1, 'away-c', 'x', false)`,
	} {
		if _, err := f.DB.Pool.Exec(ctx, q, f.TenantID); err != nil {
			t.Fatal(err)
		}
	}
	pres := presence.New(c)
	for _, sip := range []string{"idle-a", "busy-b", "away-c"} {
		t.Cleanup(func() { pres.Offline(ctx, tenant, sip) })
		pres.MarkOnline(ctx, tenant, sip)
		pres.MarkFreed(ctx, tenant, sip)
	}
	pres.MarkOnCall(ctx, tenant, "busy-b")
	freed, _ := pres.FreedAt(ctx, tenant, "idle-a")

	agents, err := s.agentsList(ctx, &Tenant{ID: f.TenantID})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*time.Time{}
	for _, a := range agents {
		got[a.SipUsername] = a.IdleSince
	}
	if got["idle-a"] == nil || !got["idle-a"].Equal(freed) {
		t.Errorf("available agent idle_since = %v, want %v", got["idle-a"], freed)
	}
	for _, sip := range []string{"busy-b", "away-c"} {
		if got[sip] != nil {
			t.Errorf("%s idle_since = %v, want none", sip, got[sip])
		}
	}
}

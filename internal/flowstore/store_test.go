package flowstore

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/dbtest"
)

const minimalFlow = `{"version":2,"entry_node":"start",
  "nodes":[{"id":"start","type":"start","config":{}},{"id":"end","type":"hangup","config":{}}],
  "edges":[{"from":"start","to":"end","condition":"default"}]}`

func insertFlow(t *testing.T, f *dbtest.Fixture, tenant uuid.UUID, name string) string {
	t.Helper()
	var id uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO flows (tenant_id, name, definition, is_active)
		VALUES ($1, $2, $3::jsonb, true)
		RETURNING id
	`, tenant, name, minimalFlow).Scan(&id); err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	return id.String()
}

// A flow id only resolves inside its own tenant: an agent who sends another
// tenant's flow_id in a join must not run (or name) that tenant's IVR.
func TestLoadIsTenantScoped(t *testing.T) {
	f := dbtest.Setup(t) // skips unless TRING_TEST_DSN set
	ctx := context.Background()
	s := New(f.DB)

	var other uuid.UUID
	if err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ('other', 'other.tring.local', $1, 'deprecated--') RETURNING id
	`, "deprecated-"+uuid.NewString()).Scan(&other); err != nil {
		t.Fatalf("insert other tenant: %v", err)
	}
	own := insertFlow(t, f, f.TenantID, "own")
	foreign := insertFlow(t, f, other, "foreign")
	me := f.TenantID.String()

	if _, err := s.Load(ctx, me, own); err != nil {
		t.Errorf("Load own flow: %v", err)
	}
	if n, err := s.FlowName(ctx, me, own); err != nil || n != "own" {
		t.Errorf("FlowName own flow = %q, %v", n, err)
	}
	if _, err := s.Load(ctx, me, foreign); err == nil {
		t.Error("Load of another tenant's flow succeeded")
	}
	if _, err := s.FlowName(ctx, me, foreign); err == nil {
		t.Error("FlowName of another tenant's flow succeeded")
	}
	if _, err := s.Load(ctx, "not-a-uuid", own); err == nil {
		t.Error("Load with a bad tenant id succeeded")
	}
}

// A deactivated inbound number stops routing calls.
func TestResolveDIDIgnoresInactiveNumbers(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	s := New(f.DB)

	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, priority, active)
		VALUES ($1, '+910000000001', 'inbound', 100, true), ($1, '+910000000002', 'inbound', 100, false)
	`, f.TenantID); err != nil {
		t.Fatalf("insert numbers: %v", err)
	}
	if _, _, err := s.ResolveDID(ctx, "+910000000001"); err != nil {
		t.Errorf("active DID: %v", err)
	}
	if _, _, err := s.ResolveDID(ctx, "+910000000002"); err == nil {
		t.Error("inactive DID still resolves")
	}
}

func liveRedis(t *testing.T) *cache.Cache {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
	if err != nil || c == nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// The DID route and the flow are served from the cache once read: a DB change
// the api didn't invalidate is not seen, and dropping the key re-reads the DB.
func TestResolveAndLoadThroughCache(t *testing.T) {
	f := dbtest.Setup(t)
	c := liveRedis(t)
	ctx := context.Background()
	s := New(f.DB).WithCache(c)

	const did = "+910000000003"
	flowID := insertFlow(t, f, f.TenantID, "cached")
	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, flow_id, priority, active)
		VALUES ($1, $2, 'inbound', $3, 100, true)
	`, f.TenantID, did, flowID); err != nil {
		t.Fatalf("insert number: %v", err)
	}
	fid := uuid.MustParse(flowID)
	_ = c.Delete(ctx, DIDCacheKey(did))
	_ = c.Delete(ctx, FlowCacheKey(fid))

	if _, got, err := s.ResolveDID(ctx, did); err != nil || got != flowID {
		t.Fatalf("cold ResolveDID = %q, %v; want %s", got, err, flowID)
	}
	if _, err := s.Load(ctx, f.TenantID.String(), flowID); err != nil {
		t.Fatalf("cold Load: %v", err)
	}

	// Change both rows behind the cache's back.
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE numbers SET flow_id = NULL WHERE e164 = $1`, did); err != nil {
		t.Fatalf("clear flow: %v", err)
	}
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE flows SET is_active = false WHERE id = $1`, flowID); err != nil {
		t.Fatalf("deactivate flow: %v", err)
	}
	if _, got, err := s.ResolveDID(ctx, did); err != nil || got != flowID {
		t.Errorf("warm ResolveDID = %q, %v; want cached %s", got, err, flowID)
	}
	if _, err := s.Load(ctx, f.TenantID.String(), flowID); err != nil {
		t.Errorf("warm Load: %v; want cached flow", err)
	}

	// What the api does on those edits.
	_ = c.Delete(ctx, DIDCacheKey(did))
	_ = c.Delete(ctx, FlowCacheKey(fid))
	if _, got, err := s.ResolveDID(ctx, did); err != nil || got != "" {
		t.Errorf("ResolveDID after invalidate = %q, %v; want no flow", got, err)
	}
	if _, err := s.Load(ctx, f.TenantID.String(), flowID); err == nil {
		t.Error("Load after invalidate served the deactivated flow")
	}
}

// A cached flow still only resolves inside its own tenant.
func TestCachedFlowIsTenantScoped(t *testing.T) {
	f := dbtest.Setup(t)
	c := liveRedis(t)
	ctx := context.Background()
	s := New(f.DB).WithCache(c)

	flowID := insertFlow(t, f, f.TenantID, "own")
	_ = c.Delete(ctx, FlowCacheKey(uuid.MustParse(flowID)))
	if _, err := s.Load(ctx, f.TenantID.String(), flowID); err != nil {
		t.Fatalf("Load own flow: %v", err)
	}
	if _, err := s.Load(ctx, uuid.NewString(), flowID); err == nil {
		t.Error("cached flow loaded for another tenant")
	}
}

// A team's settings and the tenant's hold music are cached under separate keys:
// each is served warm, and dropping one key re-reads only that half.
func TestTeamConfigThroughCache(t *testing.T) {
	f := dbtest.Setup(t)
	c := liveRedis(t)
	ctx := context.Background()
	s := New(f.DB).WithCache(c)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.DB.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO teams (tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound)
		VALUES ($1, 'sales', 'ring_all', 20, 60, 'local_stream://moh')`, f.TenantID)
	exec(`UPDATE tenants SET hold_music = 'tenant-a.ogg', transfer_failed_audio = 'fail-a.ogg' WHERE id = $1`, f.TenantID)
	teamKey, audioKey := TeamCacheKey(f.TenantID, "sales"), TenantAudioCacheKey(f.TenantID)
	_ = c.Delete(ctx, teamKey)
	_ = c.Delete(ctx, audioKey)

	cfg, ok, err := s.TeamConfig(ctx, f.TenantID, "sales")
	if err != nil || !ok || cfg.RingSeconds != 20 || cfg.MOH != "tenant-a.ogg" {
		t.Fatalf("cold TeamConfig = %+v, %v, %v", cfg, ok, err)
	}

	// Change both behind the cache's back: still served warm.
	exec(`UPDATE teams SET ring_seconds = 30 WHERE tenant_id = $1 AND name = 'sales'`, f.TenantID)
	exec(`UPDATE tenants SET hold_music = 'tenant-b.ogg', transfer_failed_audio = 'fail-b.ogg' WHERE id = $1`, f.TenantID)
	if cfg, _, _ := s.TeamConfig(ctx, f.TenantID, "sales"); cfg.RingSeconds != 20 || cfg.MOH != "tenant-a.ogg" {
		t.Errorf("warm TeamConfig = %+v, want cached", cfg)
	}
	if p, _ := s.TenantTransferFailedAudio(ctx, f.TenantID); p != "fail-a.ogg" {
		t.Errorf("warm transfer audio = %q, want cached fail-a.ogg", p)
	}

	_ = c.Delete(ctx, audioKey) // what PATCH /v1/tenants/me does
	if cfg, _, _ := s.TeamConfig(ctx, f.TenantID, "sales"); cfg.RingSeconds != 20 || cfg.MOH != "tenant-b.ogg" {
		t.Errorf("after tenant invalidate = %+v, want cached team + new hold music", cfg)
	}
	if moh, _ := s.TenantHoldMusic(ctx, f.TenantID); moh != "tenant-b.ogg" {
		t.Errorf("TenantHoldMusic = %q, want tenant-b.ogg", moh)
	}
	_ = c.Delete(ctx, teamKey) // what PATCH /v1/teams/{name} does
	if cfg, _, _ := s.TeamConfig(ctx, f.TenantID, "sales"); cfg.RingSeconds != 30 {
		t.Errorf("after team invalidate = %+v, want ring 30", cfg)
	}

	// An unknown team is ok=false and not cached.
	_ = c.Delete(ctx, TeamCacheKey(f.TenantID, "nope"))
	if _, ok, err := s.TeamConfig(ctx, f.TenantID, "nope"); ok || err != nil {
		t.Errorf("unknown team = ok %v, err %v; want not found", ok, err)
	}
	if _, hit := c.Get(ctx, TeamCacheKey(f.TenantID, "nope")); hit {
		t.Error("unknown team was cached")
	}
}

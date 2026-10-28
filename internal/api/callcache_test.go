package api

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/callrow"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/flowstore"
)

// cachedTestServer is newTestServer with a live Redis, so edits invalidate
// the gateway's cached reads. Skips when Redis isn't reachable.
func cachedTestServer(t *testing.T, f *dbtest.Fixture) (*Server, *cache.Cache) {
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
	return NewServer(Config{
		DB:               f.DB,
		JWTSecret:        "test-secret",
		GatewayPublicURL: "http://gw.test",
		Cache:            c,
	}), c
}

// warmDID resolves did through the gateway's store so its route is cached.
func warmDID(t *testing.T, f *dbtest.Fixture, c *cache.Cache, did string) {
	t.Helper()
	ctx := context.Background()
	_ = c.Delete(ctx, flowstore.DIDCacheKey(did))
	if _, _, err := flowstore.New(f.DB).WithCache(c).ResolveDID(ctx, did); err != nil {
		t.Fatalf("resolve %s: %v", did, err)
	}
	if _, ok := c.Get(ctx, flowstore.DIDCacheKey(did)); !ok {
		t.Fatalf("%s not cached after resolve", did)
	}
}

func assertDIDUncached(t *testing.T, c *cache.Cache, did string) {
	t.Helper()
	if _, ok := c.Get(context.Background(), flowstore.DIDCacheKey(did)); ok {
		t.Errorf("%s still cached", did)
	}
}

func TestPatchNumberInvalidatesDIDRoutes(t *testing.T) {
	f := dbtest.Setup(t)
	s, c := cachedTestServer(t, f)
	const oldDID, newDID = "+918005550001", "+918005550002"
	id := insertInboundWithFlowForHandlerTest(t, f, oldDID, seedFlow(t, f, "patch-cache"))
	warmDID(t, f, c, oldDID)
	// A stale route for the new e164 (e.g. from a number deleted earlier).
	c.Set(context.Background(), flowstore.DIDCacheKey(newDID), []byte(`{}`), 0)

	if rec := serve(t, s, f, "PATCH", "/v1/numbers/"+id, `{"e164":"`+newDID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	assertDIDUncached(t, c, oldDID)
	assertDIDUncached(t, c, newDID)
}

func TestDeleteNumberInvalidatesDIDRoute(t *testing.T) {
	f := dbtest.Setup(t)
	s, c := cachedTestServer(t, f)
	const did = "+918005550003"
	id := insertInboundWithFlowForHandlerTest(t, f, did, seedFlow(t, f, "delete-cache"))
	warmDID(t, f, c, did)

	if rec := serve(t, s, f, "DELETE", "/v1/numbers/"+id, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	assertDIDUncached(t, c, did)
}

// Deleting a flow drops the cached routes of the numbers that pointed at it,
// so a call to them is rejected instead of answered into a missing flow.
func TestDeleteFlowInvalidatesItsDIDRoutes(t *testing.T) {
	f := dbtest.Setup(t)
	s, c := cachedTestServer(t, f)
	const did = "+918005550004"
	flowID := seedFlow(t, f, "flow-delete-cache")
	insertInboundWithFlowForHandlerTest(t, f, did, flowID)
	warmDID(t, f, c, did)
	ctx := context.Background()
	c.Set(ctx, flowstore.FlowCacheKey(uuid.MustParse(flowID)), []byte(`{}`), 0)

	if rec := serve(t, s, f, "DELETE", "/v1/flows/"+flowID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	assertDIDUncached(t, c, did)
	if _, ok := c.Get(ctx, flowstore.FlowCacheKey(uuid.MustParse(flowID))); ok {
		t.Error("deleted flow still cached")
	}
}

func TestPatchTenantInvalidatesTenantSettings(t *testing.T) {
	f := dbtest.Setup(t)
	s, c := cachedTestServer(t, f)
	ctx := context.Background()
	keys := []string{callrow.RecordingCacheKey(f.TenantID), flowstore.TenantAudioCacheKey(f.TenantID)}
	for _, k := range keys {
		c.Set(ctx, k, []byte(`{}`), 0)
	}

	if rec := serve(t, s, f, "PATCH", "/v1/tenants/me", `{"record_calls":false}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	for _, k := range keys {
		if _, ok := c.Get(ctx, k); ok {
			t.Errorf("%s still cached", k)
		}
	}
}

func TestTeamEditsInvalidateTeamConfig(t *testing.T) {
	f := dbtest.Setup(t)
	s, c := cachedTestServer(t, f)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO teams (tenant_id, name) VALUES ($1, 'support')`, f.TenantID); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	key := flowstore.TeamCacheKey(f.TenantID, "support")

	c.Set(ctx, key, []byte(`{}`), 0)
	if rec := serve(t, s, f, "PATCH", "/v1/teams/support", `{"ring_seconds":25}`); rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := c.Get(ctx, key); ok {
		t.Error("team still cached after PATCH")
	}

	c.Set(ctx, key, []byte(`{}`), 0)
	if rec := serve(t, s, f, "DELETE", "/v1/teams/support", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := c.Get(ctx, key); ok {
		t.Error("team still cached after DELETE")
	}
}

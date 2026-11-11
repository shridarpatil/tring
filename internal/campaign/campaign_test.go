package campaign

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shridarpatil/tring/internal/dbtest"
)

func testConfig() Config {
	return Config{Type: "tts", Text: "Hello", CallerID: "+912265915000", Timezone: "UTC", WindowStart: "00:00", WindowEnd: "23:59", Concurrency: 3, CallsPerMinute: 300, MaxAttempts: 2, RetryMinutes: 1, RingSeconds: 10, MaxSeconds: 30}
}
func TestWindowsAndValidation(t *testing.T) {
	c := testConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Timezone = "America/New_York"
	c.WindowStart = "09:00"
	c.WindowEnd = "17:00"
	for _, tc := range []struct {
		at   string
		want bool
	}{{"2026-03-07T13:59:00Z", false}, {"2026-03-07T14:00:00Z", true}, {"2026-03-08T13:00:00Z", true}, {"2026-03-08T21:00:00Z", false}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if got := c.InWindow(at); got != tc.want {
			t.Errorf("%s: got %v", tc.at, got)
		}
	}
	for _, change := range []func(*Config){func(c *Config) { c.Type = "unknown" }, func(c *Config) { c.Text = "{{name}}" }, func(c *Config) { c.Concurrency = 0 }, func(c *Config) { c.MaxAttempts = 6 }, func(c *Config) { c.Timezone = "bad/zone" }, func(c *Config) { c.WindowEnd = "08:00" }} {
		bad := c
		change(&bad)
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
func fixtureCampaign(t *testing.T) (*Store, *dbtest.Fixture, uuid.UUID) {
	t.Helper()
	f := dbtest.Setup(t)
	s := New(f.DB.Pool)
	ctx := context.Background()
	id, err := s.Create(ctx, f.TenantID, "Reminders", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Prepare(ctx, f.TenantID, id, Prepared{AudioFile: "/audio/prepared.ogg"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Import(ctx, f.TenantID, id, []Recipient{{Phone: "+919876543210"}, {Phone: "+919876543211"}, {Phone: "+919876543212"}}); err != nil {
		t.Fatal(err)
	}
	return s, f, id
}
func startFixture(t *testing.T, s *Store, f *dbtest.Fixture, id uuid.UUID) {
	t.Helper()
	c, err := s.Get(context.Background(), f.TenantID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Transition(context.Background(), f.TenantID, id, "start", nil, c.Revision); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentReservationAndClaim(t *testing.T) {
	s, f, id := fixtureCampaign(t)
	startFixture(t, s, f, id)
	ctx := context.Background()
	limits := DefaultLimits()
	limits.Concurrent = 1
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Reserve(ctx, limits); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	attempts, total, err := s.Attempts(ctx, f.TenantID, id, 25, 0)
	if err != nil || total != 1 {
		t.Fatalf("attempts=%d err=%v", total, err)
	}
	attempt := attempts[0].ID
	var claims atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, claimed, err := s.Claim(ctx, f.TenantID, attempt)
			if err != nil {
				t.Error(err)
			}
			if claimed {
				claims.Add(1)
				if a.Payload.Prepared.AudioFile != "/audio/prepared.ogg" {
					t.Error("lost immutable payload")
				}
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("%d claims", claims.Load())
	}
	if err = s.Finish(ctx, f.TenantID, attempt, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, f.TenantID, attempt, "busy", "busy"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(ctx, f.TenantID, id)
	if c.Counts["completed"] != 1 || c.Counts["pending"] != 2 {
		t.Fatalf("late event changed outcome: %v", c.Counts)
	}
	if _, claimed, err := s.Claim(ctx, f.TenantID, attempt); err != nil || claimed {
		t.Fatalf("redialed settled attempt: %v %v", claimed, err)
	}
}
func TestPauseCancelAndRetry(t *testing.T) {
	s, f, id := fixtureCampaign(t)
	ctx := context.Background()
	startFixture(t, s, f, id)
	if err := s.Reserve(ctx, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	attempts, _, _ := s.Attempts(ctx, f.TenantID, id, 25, 0)
	a := attempts[0]
	c, _ := s.Get(ctx, f.TenantID, id)
	if err := s.Transition(ctx, f.TenantID, id, "pause", nil, c.Revision); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := s.Claim(ctx, f.TenantID, a.ID); err != nil || claimed {
		t.Fatalf("claimed while paused: %v %v", claimed, err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	if err := s.Transition(ctx, f.TenantID, id, "resume", nil, c.Revision); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := s.Claim(ctx, f.TenantID, a.ID); err != nil || !claimed {
		t.Fatalf("resume: %v %v", claimed, err)
	}
	if err := s.Finish(ctx, f.TenantID, a.ID, "busy", "busy"); err != nil {
		t.Fatal(err)
	}
	recipients, _, _ := s.Recipients(ctx, f.TenantID, id, 25, 0, "")
	for _, r := range recipients {
		if r.ID == *a.RecipientID && (r.Status != "pending" || r.Attempts != 1 || !r.NextAttemptAt.After(time.Now())) {
			t.Fatalf("bad retry: %+v", r)
		}
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	if err := s.Transition(ctx, f.TenantID, id, "cancel", nil, c.Revision); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	if c.Counts["cancelled"] != 3 {
		t.Fatalf("not cancelled: %v", c.Counts)
	}
	if err := s.Reserve(ctx, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	_, total, _ := s.Attempts(ctx, f.TenantID, id, 25, 0)
	if total != 1 {
		t.Fatal("cancelled campaign dialed")
	}
}
func TestSnapshotsTestIdempotencyAndIsolation(t *testing.T) {
	s, f, id := fixtureCampaign(t)
	ctx := context.Background()
	c, _ := s.Get(ctx, f.TenantID, id)
	testID, err := s.Test(ctx, f.TenantID, id, "+919876543213", c.Revision, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Test(ctx, f.TenantID, id, "+919876543213", c.Revision, DefaultLimits()); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated test request: %v", err)
	}
	if _, err = s.Get(ctx, uuid.New(), id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if _, _, err = s.Claim(ctx, uuid.New(), testID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant claim: %v", err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	cfg := c.Config
	cfg.Text = "Updated"
	if err = s.Update(ctx, f.TenantID, id, c.Name, cfg, c.Revision); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	if c.Prepared != nil {
		t.Fatal("edit retained stale preparation")
	}
	attempt, claimed, err := s.Claim(ctx, f.TenantID, testID)
	if err != nil || !claimed || attempt.Payload.Config.Text != "Hello" {
		t.Fatalf("test content mutated: %+v %v", attempt, err)
	}
	if err = s.Finish(ctx, f.TenantID, testID, "incomplete", "recipient_hangup"); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	if c.Counts["pending"] != 3 {
		t.Fatal("test altered recipients")
	}
}
func TestRecoveryDoesNotRedial(t *testing.T) {
	s, f, id := fixtureCampaign(t)
	ctx := context.Background()
	startFixture(t, s, f, id)
	if err := s.Reserve(ctx, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	attempts, _, _ := s.Attempts(ctx, f.TenantID, id, 25, 0)
	a := attempts[0]
	if _, _, err := s.Claim(ctx, f.TenantID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE campaign_attempts SET heartbeat_at=now()-interval '3 minutes' WHERE id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := s.attemptIDs(ctx, s.Q.CampaignStale, f.TenantID)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale: %v %v", stale, err)
	}
	if err = s.Finish(ctx, f.TenantID, a.ID, "unknown", "gateway lost"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(ctx, f.TenantID, id)
	if c.Counts["unknown"] != 1 {
		t.Fatal(c.Counts)
	}
	if _, claimed, err := s.Claim(ctx, f.TenantID, a.ID); err != nil || claimed {
		t.Fatal("recovery redialed uncertain attempt")
	}
	if err = s.Heartbeat(ctx, f.TenantID, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale gateway retained ownership: %v", err)
	}
}

func TestGatewayPacesQueuedCallsAfterOutage(t *testing.T) {
	s, f, id := fixtureCampaign(t)
	ctx := context.Background()
	c, _ := s.Get(ctx, f.TenantID, id)
	first, err := s.Test(ctx, f.TenantID, id, "+919876543210", c.Revision, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	second, err := s.Test(ctx, f.TenantID, id, "+919876543211", c.Revision, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	s.Limits.StartsPerSecond = 1
	if _, claimed, err := s.Claim(ctx, f.TenantID, first); err != nil || !claimed {
		t.Fatalf("first claim %v %v", claimed, err)
	}
	if _, claimed, err := s.Claim(ctx, f.TenantID, second); err != nil || claimed {
		t.Fatalf("burst after outage: %v %v", claimed, err)
	}
	if _, err = f.DB.Pool.Exec(ctx, "UPDATE campaign_attempts SET started_at=now()-interval '2 seconds' WHERE id=$1", first); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := s.Claim(ctx, f.TenantID, second); err != nil || !claimed {
		t.Fatalf("next slot %v %v", claimed, err)
	}
}

func TestSchedulerUsesOneConnectionPerTransaction(t *testing.T) {
	s, f, id := fixtureCampaign(t)
	startFixture(t, s, f, id)
	cfg := f.DB.Pool.Config()
	cfg.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	single := New(pool)
	if err = single.Reserve(ctx, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	attempts, _, err := single.Attempts(ctx, f.TenantID, id, 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := single.Claim(ctx, f.TenantID, attempts[0].ID); err != nil || !claimed {
		t.Fatalf("claim with one connection: %v %v", claimed, err)
	}
	// A stale recovery candidate must be rechecked after locking, in case a
	// heartbeat arrived between selection and settlement.
	if err = single.Finish(ctx, f.TenantID, attempts[0].ID, "unknown", "stale selection"); err != nil {
		t.Fatal(err)
	}
	if err = single.Heartbeat(ctx, f.TenantID, attempts[0].ID); err != nil {
		t.Fatalf("fresh heartbeat was overwritten by recovery: %v", err)
	}
}

func TestFutureScheduleAndCancellationLeaveActiveCallAlone(t *testing.T) {
	s, f, id := fixtureCampaign(t)
	ctx := context.Background()
	c, _ := s.Get(ctx, f.TenantID, id)
	at := time.Now().Add(time.Hour)
	if err := s.Transition(ctx, f.TenantID, id, "schedule", &at, c.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(ctx, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	_, total, err := s.Attempts(ctx, f.TenantID, id, 25, 0)
	if err != nil || total != 0 {
		t.Fatalf("dialed before scheduled start: %d %v", total, err)
	}
	if _, err = f.DB.Pool.Exec(ctx, "UPDATE campaigns SET scheduled_at=now()-interval '1 minute' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err = s.Reserve(ctx, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	attempts, _, err := s.Attempts(ctx, f.TenantID, id, 25, 0)
	if err != nil || len(attempts) != 1 {
		t.Fatal(attempts, err)
	}
	a := attempts[0]
	if _, claimed, err := s.Claim(ctx, f.TenantID, a.ID); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	if err = s.Transition(ctx, f.TenantID, id, "cancel", nil, c.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.Heartbeat(ctx, f.TenantID, a.ID); err != nil {
		t.Fatalf("cancel interrupted active call: %v", err)
	}
	if err = s.Finish(ctx, f.TenantID, a.ID, "busy", "busy"); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, f.TenantID, id)
	if c.Status != "cancelled" || c.Counts["cancelled"] != 2 || c.Counts["busy"] != 1 || c.Counts["pending"] != 0 {
		t.Fatalf("cancelled call retried: %+v", c)
	}
}

func TestAgentCampaignValidation(t *testing.T) {
	c := testConfig()
	c.Type, c.Text, c.Team, c.AgentWaitSeconds = "agent", "", "sales", 120
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.Team = " " }, func(c *Config) { c.AgentWaitSeconds = 0 }, func(c *Config) { c.AgentWaitSeconds = 3601 }} {
		bad := c
		change(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted invalid agent campaign: %+v", bad)
		}
	}
}

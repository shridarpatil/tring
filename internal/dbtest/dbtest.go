// Package dbtest is the shared test harness for tests that need a real
// Postgres. It is import-only from *_test.go files.
//
// Tests opt in by calling Setup(t), which:
//
//  1. Skips the test if TRING_TEST_DSN is unset, so `go test ./...` on a
//     dev box without Postgres still passes (only the DB-dependent tests
//     skip, not the pure-unit ones).
//  2. Connects via pgx and TRUNCATEs the tables a test could touch, so
//     test ordering doesn't matter.
//  3. Inserts a deterministic fixture tenant + API key and returns the
//     handle so handler tests can authenticate.
//
// The CI workflow spins up a Postgres service container, applies
// db/schema.sql once at startup, and exports TRING_TEST_DSN. Locally,
// `docker compose up -d postgres` then exporting the same DSN works too.
package dbtest

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/db"
	tringmigrate "github.com/shridarpatil/tring/internal/migrate"
)

// databaseName extracts the database name from a postgres DSN
// (the last path segment of postgres://user:pass@host:port/<db>).
func databaseName(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

// Fixture is what Setup returns: a connected DB plus a seeded tenant +
// admin console user so handler tests can mint a JWT and authenticate.
type Fixture struct {
	DB        *db.DB
	TenantID  uuid.UUID
	SIPDomain string

	// UserID and UserEmail identify the seeded console user. Tests use
	// these to mint a JWT via (*api.Server).IssueConsoleToken and put it
	// in the Authorization header.
	UserID    uuid.UUID
	UserEmail string
}

const (
	// dbtestUserEmail is the email of the seeded console user. The
	// password is irrelevant — tests mint tokens directly via the
	// server's token issuer rather than going through /console/login.
	dbtestUserEmail = "dbtest@tring.local"
)

// Setup returns a fresh DB handle with all tables TRUNCATEd and a single
// tenant row inserted. Caller must `defer cleanup()` via t.Cleanup.
func Setup(t *testing.T) *Fixture {
	t.Helper()
	dsn := os.Getenv("TRING_TEST_DSN")
	if dsn == "" {
		t.Skip("TRING_TEST_DSN not set; skipping DB-dependent test")
	}

	// Safety guard: Setup TRUNCATEs every table. If TRING_TEST_DSN is
	// accidentally pointed at a dev/prod database, that's silent data loss.
	// Require the target database name to contain "test" so a destructive
	// run can only hit a database explicitly named for testing.
	if name := databaseName(dsn); !strings.Contains(name, "test") {
		t.Skipf("refusing destructive truncate: TRING_TEST_DSN database %q must contain 'test' in its name "+
			"(point it at a dedicated test DB, e.g. tring_test)", name)
	}

	// Apply migrations before doing anything else so the test database
	// is at HEAD — schema.sql is not the source of truth any more, the
	// embedded migrations in internal/migrate/migrations are. Idempotent;
	// repeated Setup() calls are no-ops after the first.
	if err := tringmigrate.Run(dsn); err != nil {
		t.Fatalf("migrate.Run: %v", err)
	}

	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Connect: %v", err)
	}
	t.Cleanup(func() { d.Pool.Close() })

	// RESTART IDENTITY isn't relevant here (all PKs are UUIDs) but CASCADE
	// flushes everything that FKs into tenants too.
	// `users` joins this list so the seeded console user gets cleared too.
	_, err = d.Pool.Exec(ctx, `
		TRUNCATE tenants, agents, numbers, calls, webhooks, webhook_deliveries, users, flows, trunks
		RESTART IDENTITY CASCADE;
	`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// api_key_hash / api_key_prefix are NOT NULL UNIQUE on the v0.1
	// schema but the auth flow has moved to console JWTs and no longer
	// consults them. Stamp a per-fixture sentinel so the inserts
	// succeed across parallel/sequential test runs.
	apiKeySentinel := "deprecated-" + uuid.New().String()
	var tenantID uuid.UUID
	const sipDomain = "test.tring.local"
	err = d.Pool.QueryRow(ctx, `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "dbtest", sipDomain, apiKeySentinel, "deprecated--").Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	// Seed a console user — password_hash is a fixed bcrypt sentinel
	// that no test ever validates against, since tests bypass the
	// /console/login bcrypt check and mint JWTs directly.
	const bcryptSentinel = "$2a$10$dbtest.dbtest.dbtest.dbtest.dbtest.dbtest.dbtest.dbtest.dbtest"
	var userID uuid.UUID
	err = d.Pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id
	`, tenantID, dbtestUserEmail, bcryptSentinel).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	return &Fixture{
		DB:        d,
		TenantID:  tenantID,
		SIPDomain: sipDomain,
		UserID:    userID,
		UserEmail: dbtestUserEmail,
	}
}

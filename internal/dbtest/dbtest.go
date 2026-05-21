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

	"github.com/whatomate/tring/internal/db"
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

// Fixture is what Setup returns: a connected DB plus the tenant row it
// inserted, so tests can authenticate without re-running INSERTs.
type Fixture struct {
	DB         *db.DB
	TenantID   uuid.UUID
	APIKey     string // plaintext, for Authorization: Bearer <key>
	APIKeyHash string
	SIPDomain  string
}

const (
	// Plaintext key used in tests. The hash is computed from this and
	// inserted into tenants.api_key_hash so AuthRequired finds it.
	testAPIKey = "tk_test_dbtest_fixture_key_do_not_use_in_prod"
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

	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Connect: %v", err)
	}
	t.Cleanup(func() { d.Pool.Close() })

	// RESTART IDENTITY isn't relevant here (all PKs are UUIDs) but CASCADE
	// flushes everything that FKs into tenants too.
	_, err = d.Pool.Exec(ctx, `
		TRUNCATE tenants, agents, numbers, calls, webhooks, webhook_deliveries
		RESTART IDENTITY CASCADE;
	`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// SHA-256 of testAPIKey, precomputed to avoid pulling api.HashKey
	// (which would create an import cycle: api → dbtest → api).
	keyHash := sha256Hex(testAPIKey)

	var tenantID uuid.UUID
	const sipDomain = "test.tring.local"
	err = d.Pool.QueryRow(ctx, `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "dbtest", sipDomain, keyHash, testAPIKey[:12]).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	return &Fixture{
		DB:         d,
		TenantID:   tenantID,
		APIKey:     testAPIKey,
		APIKeyHash: keyHash,
		SIPDomain:  sipDomain,
	}
}

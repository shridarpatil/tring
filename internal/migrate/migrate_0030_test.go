package migrate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// migrateIsolated migrates a throwaway schema (via search_path) in the test
// database to version, so a test can seed pre-migration rows and then apply
// the next migration without touching the shared public schema.
func migrateIsolated(t *testing.T, version uint) string {
	t.Helper()
	base := os.Getenv("TRING_TEST_DSN")
	if base == "" {
		t.Skip("TRING_TEST_DSN not set")
	}
	u, err := url.Parse(base)
	if err != nil || !strings.Contains(strings.TrimPrefix(u.Path, "/"), "test") {
		t.Fatalf("TRING_TEST_DSN database name must contain \"test\"")
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "mig0030_" + hex.EncodeToString(b[:])

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(ctx)
	})

	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn := u.String()
	migrateTo(t, dsn, version)
	return dsn
}

func migrateTo(t *testing.T, dsn string, version uint) {
	t.Helper()
	m, err := open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Migrate(version); err != nil {
		t.Fatalf("migrate to %d: %v", version, err)
	}
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn) // pgx forwards search_path as a runtime param
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	return conn
}

func mustExec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func seedTenant(t *testing.T, c *pgx.Conn, name string) string {
	t.Helper()
	var id string
	err := c.QueryRow(context.Background(),
		`INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix) VALUES ($1, $2, $3, $4) RETURNING id`,
		name, name+".example.com", "h-"+name, "p-"+name).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigration0030MovesConsoleUsers(t *testing.T) {
	dsn := migrateIsolated(t, 29)
	c := connect(t, dsn)
	ctx := context.Background()
	tenant := seedTenant(t, c, "t1")
	var a1, u1 string
	if err := c.QueryRow(ctx, `INSERT INTO agents (tenant_id, sip_username, sip_password) VALUES ($1, 'alice', 'pw') RETURNING id`, tenant).Scan(&a1); err != nil {
		t.Fatal(err)
	}
	if err := c.QueryRow(ctx, `INSERT INTO users (tenant_id, email, password_hash, can_supervise) VALUES ($1, 'Admin@Example.com', 'h', true) RETURNING id`, tenant).Scan(&u1); err != nil {
		t.Fatal(err)
	}
	mustExec(t, c, `INSERT INTO teams (tenant_id, name) VALUES ($1, 'sales')`, tenant)
	mustExec(t, c, `INSERT INTO team_agents (tenant_id, team_name, agent_id, position) VALUES ($1, 'sales', $2, 0)`, tenant, a1)
	mustExec(t, c, `INSERT INTO supervisor_sessions (tenant_id, supervisor_user_id) VALUES ($1, $2)`, tenant, u1)

	migrateTo(t, dsn, 30)

	var n int
	if err := c.QueryRow(ctx, `SELECT count(*) FROM users WHERE tenant_id = $1`, tenant).Scan(&n); err != nil || n != 2 {
		t.Fatalf("users = %d, %v; want 2", n, err)
	}
	var role, email, hash, sip, pw string
	if err := c.QueryRow(ctx, `SELECT role, email, password_hash, sip_username, sip_password FROM users WHERE id = $1`, u1).
		Scan(&role, &email, &hash, &sip, &pw); err != nil {
		t.Fatal(err)
	}
	if role != "admin" || email != "admin@example.com" || hash != "h" || sip != "admin@example.com" || pw == "" {
		t.Errorf("admin row = %q %q %q %q %q", role, email, hash, sip, pw)
	}
	var email2, hash2 *string
	if err := c.QueryRow(ctx, `SELECT role, email, password_hash FROM users WHERE id = $1`, a1).Scan(&role, &email2, &hash2); err != nil {
		t.Fatal(err)
	}
	if role != "agent" || email2 != nil || hash2 != nil {
		t.Errorf("agent row = %q %v %v", role, email2, hash2)
	}
	var mgr bool
	if err := c.QueryRow(ctx, `SELECT is_manager FROM team_agents WHERE agent_id = $1`, a1).Scan(&mgr); err != nil || mgr {
		t.Errorf("is_manager = %v, %v; want false", mgr, err)
	}
	var sess string
	if err := c.QueryRow(ctx, `SELECT supervisor_user_id FROM supervisor_sessions WHERE tenant_id = $1`, tenant).Scan(&sess); err != nil || sess != u1 {
		t.Errorf("supervisor_user_id = %q, %v; want %s", sess, err, u1)
	}
	var gone bool
	if err := c.QueryRow(ctx, `SELECT to_regclass('agents') IS NULL`).Scan(&gone); err != nil || !gone {
		t.Errorf("agents table still exists (%v)", err)
	}
	// The FK must be live again: a dangling supervisor id is rejected.
	if _, err := c.Exec(ctx, `INSERT INTO supervisor_sessions (tenant_id, supervisor_user_id) VALUES ($1, gen_random_uuid())`, tenant); err == nil {
		t.Error("supervisor_sessions FK to users not restored")
	}
}

func TestMigration0030SipUsernameCollision(t *testing.T) {
	dsn := migrateIsolated(t, 29)
	c := connect(t, dsn)
	ctx := context.Background()
	tenant := seedTenant(t, c, "t1")
	mustExec(t, c, `INSERT INTO agents (tenant_id, sip_username, sip_password) VALUES ($1, 'boss@example.com', 'pw')`, tenant)
	var uid string
	if err := c.QueryRow(ctx, `INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, 'boss@example.com', 'h') RETURNING id`, tenant).Scan(&uid); err != nil {
		t.Fatal(err)
	}

	migrateTo(t, dsn, 30)

	var n int
	if err := c.QueryRow(ctx, `SELECT count(*) FROM users WHERE tenant_id = $1`, tenant).Scan(&n); err != nil || n != 2 {
		t.Fatalf("users = %d, %v; want 2", n, err)
	}
	var sip string
	if err := c.QueryRow(ctx, `SELECT sip_username FROM users WHERE id = $1`, uid).Scan(&sip); err != nil {
		t.Fatal(err)
	}
	if want := "boss@example.com-" + uid[:8]; sip != want {
		t.Errorf("sip_username = %q, want %q", sip, want)
	}
}

func TestMigration0030EmailUniqueCaseInsensitive(t *testing.T) {
	dsn := migrateIsolated(t, 30)
	c := connect(t, dsn)
	t1, t2 := seedTenant(t, c, "t1"), seedTenant(t, c, "t2")
	ins := `INSERT INTO users (tenant_id, sip_username, sip_password, email) VALUES ($1, $2, 'x', $3)`
	mustExec(t, c, ins, t1, "a", "x@example.com")
	_, err := c.Exec(context.Background(), ins, t2, "b", "X@example.com")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second insert err = %v, want unique violation", err)
	}
}

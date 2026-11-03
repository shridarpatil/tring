package migrate

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestLatest(t *testing.T) {
	got, err := Latest()
	if err != nil {
		t.Fatal(err)
	}
	// The highest NNNN prefix among the embedded files.
	var want uint
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		v, err := strconv.ParseUint(strings.SplitN(strings.TrimPrefix(n, "migrations/"), "_", 2)[0], 10, 32)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		want = max(want, uint(v))
	}
	if got < 29 || got != want {
		t.Errorf("Latest() = %d, want %d (>= 29)", got, want)
	}
}

// testDSN returns TRING_TEST_DSN migrated to the latest version, or skips.
// Like internal/dbtest, it refuses a database whose name lacks "test".
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TRING_TEST_DSN")
	if dsn == "" {
		t.Skip("TRING_TEST_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.Contains(strings.TrimPrefix(u.Path, "/"), "test") {
		t.Fatalf("TRING_TEST_DSN database name must contain \"test\"")
	}
	if err := Run(dsn); err != nil {
		t.Fatal(err)
	}
	return dsn
}

// setSchema overwrites schema_migrations for one test and restores it after.
func setSchema(t *testing.T, dsn, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		latest, _ := Latest()
		_, _ = conn.Exec(ctx, "UPDATE schema_migrations SET version = $1, dirty = false", latest)
		conn.Close(ctx)
	})
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestReadyUpToDate(t *testing.T) {
	dsn := testDSN(t)
	if err := Ready(dsn); err != nil {
		t.Fatalf("Ready() = %v, want nil", err)
	}
}

func TestReadyBehind(t *testing.T) {
	dsn := testDSN(t)
	latest, err := Latest()
	if err != nil {
		t.Fatal(err)
	}
	setSchema(t, dsn, "UPDATE schema_migrations SET version = $1", latest-1)
	if err := Ready(dsn); !errors.Is(err, ErrSchemaBehind) {
		t.Fatalf("Ready() = %v, want ErrSchemaBehind", err)
	}
}

func TestReadyDirty(t *testing.T) {
	dsn := testDSN(t)
	setSchema(t, dsn, "UPDATE schema_migrations SET dirty = true")
	if err := Ready(dsn); !errors.Is(err, ErrSchemaDirty) {
		t.Fatalf("Ready() = %v, want ErrSchemaDirty", err)
	}
}

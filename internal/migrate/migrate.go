// Package migrate applies versioned schema migrations on api startup.
//
// Migrations live in internal/migrate/migrations/ as a numbered pair of
// SQL files per version (NNNN_name.up.sql / NNNN_name.down.sql). They're
// embedded into the api binary via //go:embed, so a single `docker
// compose pull` of a newer image ships any new migrations with it; no
// separate "run migrate" step on the operator's side.
//
// On startup, Run() reads the embedded files and calls migrate.Up(),
// which is idempotent: it tracks applied versions in a `schema_migrations`
// table and only runs migrations newer than that. Empty DBs get the full
// initial schema; up-to-date DBs are a no-op.
package migrate

import (
	"embed"
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Run applies all pending migrations. Returns nil on success or when
// there are no pending changes (ErrNoChange). Any other error is fatal
// and the caller should not start the server.
func Run(dsn string) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrate: load embedded sources: %w", err)
	}

	// golang-migrate's postgres driver uses lib/pq, which defaults to
	// requiring SSL. The dev Postgres (and most plain self-hosted
	// setups) don't have TLS configured. Inject sslmode=disable if the
	// caller didn't pass an explicit sslmode in the DSN.
	dsn, err = ensureSSLMode(dsn)
	if err != nil {
		return fmt.Errorf("migrate: parse dsn: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("migrate: new instance: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}

// ensureSSLMode returns the DSN with sslmode=disable added if no
// sslmode is set. Production deploys with TLS should pass `?sslmode=
// require` (or stricter) in TRING_DSN — this only fills in the gap
// for the common dev/self-host case where TLS isn't configured.
func ensureSSLMode(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if q.Get("sslmode") == "" {
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// Package migrate applies versioned schema migrations on api-role startup,
// and tells a gateway-only node whether the schema is ready for it.
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
	"io/fs"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	// ErrSchemaBehind: the database lacks migrations this binary embeds. On a
	// gateway-only node that means the main node hasn't migrated yet.
	ErrSchemaBehind = errors.New("migrate: database schema is behind this binary")
	// ErrSchemaDirty: a migration failed part-way and needs an operator.
	ErrSchemaDirty = errors.New("migrate: database schema is dirty (a migration failed)")
)

// Run applies all pending migrations. Returns nil on success or when
// there are no pending changes (ErrNoChange). Any other error is fatal
// and the caller should not start the server.
//
// Concurrent runs are safe: Up holds the postgres driver's
// pg_advisory_lock for its duration, so two api nodes (or a restart
// overlap) can't both migrate.
func Run(dsn string) error {
	m, err := open(dsn)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}

// Latest returns the highest migration version embedded in this binary.
func Latest() (uint, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return 0, fmt.Errorf("migrate: load embedded sources: %w", err)
	}
	defer src.Close()
	v, err := src.First()
	if err != nil {
		return 0, fmt.Errorf("migrate: first version: %w", err)
	}
	for {
		next, err := src.Next(v)
		if errors.Is(err, fs.ErrNotExist) {
			return v, nil
		}
		if err != nil {
			return 0, fmt.Errorf("migrate: next version: %w", err)
		}
		v = next
	}
}

// Ready reports whether the database has every migration this binary
// embeds, without changing anything: nil, ErrSchemaBehind (wrapped with
// both versions), ErrSchemaDirty, or a connect error. A schema ahead of
// the binary counts as ready (see the upgrade order in the release notes).
func Ready(dsn string) error {
	latest, err := Latest()
	if err != nil {
		return err
	}
	m, err := open(dsn)
	if err != nil {
		return err
	}
	defer m.Close()

	v, dirty, err := m.Version()
	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		return fmt.Errorf("%w: database has no schema, binary needs %d", ErrSchemaBehind, latest)
	case err != nil:
		return fmt.Errorf("migrate: read version: %w", err)
	case dirty:
		return fmt.Errorf("%w: at version %d", ErrSchemaDirty, v)
	case v < latest:
		return fmt.Errorf("%w: database at %d, binary needs %d", ErrSchemaBehind, v, latest)
	}
	return nil
}

func open(dsn string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: load embedded sources: %w", err)
	}

	// golang-migrate's postgres driver uses lib/pq, which defaults to
	// requiring SSL. The dev Postgres (and most plain self-hosted
	// setups) don't have TLS configured. Inject sslmode=disable if the
	// caller didn't pass an explicit sslmode in the DSN.
	dsn, err = ensureSSLMode(dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: parse dsn: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: new instance: %w", err)
	}
	return m, nil
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

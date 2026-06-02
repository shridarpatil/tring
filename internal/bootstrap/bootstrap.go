// Package bootstrap creates the initial tenant + admin user from env
// vars the first time the api boots against an empty database.
//
// Design: the operator sets TRING_BOOTSTRAP_TENANT / TRING_ADMIN_EMAIL /
// TRING_ADMIN_PASSWORD in their .env. On every startup the api checks
// `tenants` — if empty AND the env vars are present, it seeds one
// tenant + one console user with a bcrypt-hashed password. On every
// startup after that, this is a no-op (idempotent by `tenants empty`
// guard, not by env-equality, so changing the env doesn't re-seed).
//
// This replaces the old `--seed-tenant` CLI step for the docker-compose
// flow: a fresh `docker compose up -d` lands a working console with
// known credentials without the operator running anything extra.
package bootstrap

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/queries"
)

// Config is everything the seed needs. All three fields are required
// together — if any is empty, Run does nothing (so the api can be
// deployed without bootstrap, e.g. when the tenant was created some
// other way).
type Config struct {
	TenantName    string
	AdminEmail    string
	AdminPassword string
}

// Run seeds the initial tenant + admin user if the database has no
// tenants yet. Returns nil on success or when seeding was skipped (no
// config, or tenants already exist). Database / hashing errors are
// fatal — main should refuse to start if Run errors.
func Run(ctx context.Context, database *db.DB, cfg Config) error {
	if cfg.TenantName == "" || cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return nil
	}
	q := queries.MustLoad()

	var count int
	if err := database.Pool.QueryRow(ctx, q.CountTenants).Scan(&count); err != nil {
		return fmt.Errorf("bootstrap: count tenants: %w", err)
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("bootstrap: hash password: %w", err)
	}

	sipDomain := "t_" + strings.ToLower(strings.ReplaceAll(cfg.TenantName, " ", "_")) + ".tring.local"
	email := strings.ToLower(cfg.AdminEmail)

	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// api_key_hash / api_key_prefix are NOT NULL UNIQUE on the v0.1
	// schema but the auth flow no longer consults them. Stamp unique
	// sentinels so the inserts succeed; a future migration can drop
	// those columns entirely. Each tenant gets a different sentinel so
	// the UNIQUE constraint stays happy across multi-tenant deploys.
	apiKeySentinel := "deprecated-" + uuid.New().String()
	var tenantID uuid.UUID
	err = tx.QueryRow(ctx, q.InsertTenant,
		cfg.TenantName, apiKeySentinel, "deprecated--", sipDomain).Scan(&tenantID)
	if err != nil {
		return fmt.Errorf("bootstrap: insert tenant: %w", err)
	}

	if _, err := tx.Exec(ctx, q.InsertUser, tenantID, email, string(hash)); err != nil {
		return fmt.Errorf("bootstrap: insert user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("bootstrap: commit: %w", err)
	}

	// Operator already knows the password — they set it in env. Just
	// confirm what was seeded so they can verify on first boot.
	log.Printf("bootstrap: seeded tenant %q (id=%s)", cfg.TenantName, tenantID)
	log.Printf("bootstrap: admin user %s — sign in at /admin/", email)
	return nil
}

package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/secretbox"
)

// SIP passwords (trunks.sip_password, agents.sip_password) are stored sealed
// with TRING_SECRETS_KEY (internal/secretbox) and opened only where they are
// handed to FreeSWITCH: the gateway XML (rerenderAllGateways) and the agent
// directory (handleFSDirectory). With no key configured (s.secrets == nil)
// new values are stored as plaintext and only plaintext can be read — the
// upgrade path for deployments that haven't added the key yet. cmd/api
// refuses to start keyless if sealed values exist.

// errNoSecretsKey: a sealed value was read but no key is configured.
var errNoSecretsKey = errors.New("value is encrypted but TRING_SECRETS_KEY is not set")

// sealSecret prepares a SIP password for storage.
func (s *Server) sealSecret(plaintext string) string {
	if s.secrets == nil {
		return plaintext
	}
	return s.secrets.Seal(plaintext)
}

// sealSecretPtr is sealSecret for an optional PATCH field (nil = unchanged).
func (s *Server) sealSecretPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := s.sealSecret(*p)
	return &v
}

// openSecret recovers a stored SIP password for use.
func (s *Server) openSecret(stored string) (string, error) {
	if s.secrets == nil {
		if secretbox.IsSealed(stored) {
			return "", errNoSecretsKey
		}
		return stored, nil
	}
	return s.secrets.Open(stored)
}

// PrepareStoredSecrets runs once at api startup, after migrations.
//
//   - Key set (box != nil): seals every legacy plaintext SIP password.
//   - No key: refuses (returns an error) if any stored password is already
//     sealed — they could not be decrypted. Otherwise warns and carries on
//     in plaintext mode, so a deployment that pulls a new image before adding
//     TRING_SECRETS_KEY keeps working.
//
// Logs counts only, never values.
func PrepareStoredSecrets(ctx context.Context, database *db.DB, box *secretbox.Box, log *slog.Logger) error {
	if box != nil {
		n, err := sealLegacySecrets(ctx, database, box)
		if err != nil {
			return fmt.Errorf("encrypt legacy SIP passwords: %w", err)
		}
		if n > 0 {
			log.Info("encrypted legacy plaintext SIP passwords at rest", "count", n)
		}
		return nil
	}
	sealed, plaintext, err := countStoredSecrets(ctx, database)
	if err != nil {
		return fmt.Errorf("count stored SIP passwords: %w", err)
	}
	if sealed > 0 {
		return fmt.Errorf("%d stored SIP passwords are encrypted but TRING_SECRETS_KEY is not set — restore the key from your backup", sealed)
	}
	log.Warn("TRING_SECRETS_KEY is not set: trunk and agent SIP passwords are stored in PLAINTEXT. "+
		"Generate one (od -An -tx1 -N32 /dev/urandom | tr -d ' \\n'), add it to .env, back it up, and restart",
		"plaintext_count", plaintext)
	return nil
}

// countStoredSecrets returns how many stored SIP passwords (trunks + agents)
// are sealed and how many are still legacy plaintext.
func countStoredSecrets(ctx context.Context, database *db.DB) (sealed, plaintext int, err error) {
	err = database.Pool.QueryRow(ctx, queries.MustLoad().CountStoredSecrets).Scan(&sealed, &plaintext)
	return sealed, plaintext, err
}

// sealLegacySecrets encrypts every plaintext SIP password (trunks + agents)
// in one transaction and returns how many it sealed. Idempotent: sealed
// values are skipped, and the rows are locked so concurrent api replicas
// starting together don't double-seal.
func sealLegacySecrets(ctx context.Context, database *db.DB, box *secretbox.Box) (int, error) {
	q := queries.MustLoad()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type legacy struct {
		id uuid.UUID
		pw string
	}
	n := 0
	for _, tbl := range []struct{ name, list, set string }{
		{"trunks", q.ListPlaintextTrunkSecrets, q.SetTrunkSecret},
		{"agents", q.ListPlaintextAgentSecrets, q.SetAgentSecret},
	} {
		rows, err := tx.Query(ctx, tbl.list)
		if err != nil {
			return 0, fmt.Errorf("list plaintext %s: %w", tbl.name, err)
		}
		var todo []legacy
		for rows.Next() {
			var l legacy
			if err := rows.Scan(&l.id, &l.pw); err != nil {
				rows.Close()
				return 0, fmt.Errorf("scan %s: %w", tbl.name, err)
			}
			todo = append(todo, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("list plaintext %s: %w", tbl.name, err)
		}
		for _, l := range todo {
			if _, err := tx.Exec(ctx, tbl.set, l.id, box.Seal(l.pw)); err != nil {
				return 0, fmt.Errorf("seal %s row: %w", tbl.name, err)
			}
		}
		n += len(todo)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

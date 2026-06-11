// Package flowstore is a read-only loader for IVR flow definitions by id, used
// by the media gateway to run the IVR on the pion plane (internal/media/ivrdrive)
// without depending on the orchestrator. It reuses the shared SQL catalog and
// the transport-agnostic flow model (internal/ivr).
package flowstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/queries"
)

// Store loads active flows by id. Read-only: it never mutates the DB.
type Store struct {
	db *db.DB
	q  *queries.Queries
}

// New builds a Store over an existing DB handle.
func New(d *db.DB) *Store {
	return &Store{db: d, q: queries.MustLoad()}
}

// Load fetches the active flow with the given id and parses its definition.
// Returns an error for a bad id, an unknown/inactive flow, or a malformed
// definition; (nil, nil) if the flow row exists but has an empty definition.
func (s *Store) Load(ctx context.Context, flowID string) (*ivr.Flow, error) {
	fid, err := uuid.Parse(flowID)
	if err != nil {
		return nil, fmt.Errorf("bad flow id %q: %w", flowID, err)
	}
	var (
		tenantID uuid.UUID
		name     string
		def      string
	)
	row := s.db.Pool.QueryRow(ctx, s.q.GetActiveFlowByID, fid)
	if err := row.Scan(&tenantID, &name, &def); err != nil {
		return nil, fmt.Errorf("load flow %s: %w", flowID, err)
	}
	return ivr.Parse([]byte(def))
}

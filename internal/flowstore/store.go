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

// ResolveDID returns the owning tenant + attached flow id for an inbound DID
// (empty flowID = the number exists but has no flow attached). Used by the media
// plane to route an inbound phone call (the PSTN plug) to its IVR. Read-only.
func (s *Store) ResolveDID(ctx context.Context, did string) (tenantID uuid.UUID, flowID string, err error) {
	var fid *uuid.UUID
	row := s.db.Pool.QueryRow(ctx, s.q.ResolveInboundNumber, did)
	if err := row.Scan(&tenantID, &fid); err != nil {
		return uuid.Nil, "", fmt.Errorf("resolve DID %s: %w", did, err)
	}
	if fid != nil {
		flowID = fid.String()
	}
	return tenantID, flowID, nil
}

// TeamSIPs returns the SIP usernames of a team's members that are available for
// calls, in membership position order. Used by the media gateway's transfer ring
// to target the right agents (parity with the FreeSWITCH executor, which uses
// the same query). Read-only.
func (s *Store) TeamSIPs(ctx context.Context, tenantID uuid.UUID, team string) ([]string, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamAgentSips, tenantID, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sips []string
	for rows.Next() {
		var sip string
		var pos int
		if err := rows.Scan(&sip, &pos); err != nil {
			return nil, err
		}
		sips = append(sips, sip)
	}
	return sips, rows.Err()
}

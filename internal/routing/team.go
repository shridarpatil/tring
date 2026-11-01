package routing

import (
	"context"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/queries"
)

// TeamConfig is the queue-relevant settings of a team.
type TeamConfig struct {
	Name        string
	Strategy    string // ring_all | round_robin | sequential | longest_wait
	RingSeconds int    // per-attempt ring timeout
	MaxWait     int    // total queue timeout (seconds)
	SLASeconds  int    // max seconds a caller should wait in queue (0 = no SLA)
	MOH         string // hold music: the team's own from LoadTeamConfig, resolved via EffectiveMOH
}

// LoadTeamConfig fetches a team's own queue settings. MOH is the team's
// moh_sound, unresolved — combine it with the tenant's hold music via
// EffectiveMOH. Returns pgx.ErrNoRows when the team doesn't exist.
func LoadTeamConfig(ctx context.Context, d *db.DB, q *queries.Queries, tenantID uuid.UUID, team string) (TeamConfig, error) {
	cfg := TeamConfig{Name: team}
	err := d.Pool.QueryRow(ctx, q.GetTeamMOH, tenantID, team).Scan(
		&cfg.Strategy, &cfg.RingSeconds, &cfg.MaxWait, &cfg.MOH, &cfg.SLASeconds)
	return cfg, err
}

// EffectiveMOH resolves the hold music a team plays: an explicit per-team
// override wins; otherwise the tenant's uploaded system hold music; otherwise
// the built-in default stream. teams.moh_sound defaults to 'local_stream://moh'
// in the schema, so that value (or empty) means "no team override".
func EffectiveMOH(teamMOH, tenantHoldMusic string) string {
	const builtin = "local_stream://moh"
	if teamMOH != "" && teamMOH != builtin {
		return teamMOH
	}
	if tenantHoldMusic != "" {
		return tenantHoldMusic
	}
	return builtin
}

// TeamMembers returns a team's member SIP usernames in position order
// (available_for_calls only), regardless of live presence. Empty = no members.
// One definition shared by the FreeSWITCH executor and the pion flowstore so the
// team-membership lookup can't drift.
func TeamMembers(ctx context.Context, d *db.DB, q *queries.Queries, tenantID uuid.UUID, team string) ([]string, error) {
	rows, err := d.Pool.Query(ctx, q.ListTeamAgentSips, tenantID, team)
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

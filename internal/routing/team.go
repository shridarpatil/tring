package routing

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/queries"
)

// TeamConfig is the queue-relevant settings of a team.
type TeamConfig struct {
	Name        string
	Strategy    string // ring_all | round_robin | sequential | longest_wait
	RingSeconds int    // per-attempt ring timeout
	MaxWait     int    // total queue timeout (seconds)
	MOH         string // resolved hold music
}

// LoadTeamConfig fetches a team's queue settings and resolves its effective hold
// music; ok=false when the team doesn't exist. Shared by both planes so the web
// and phone sides honor the same team strategy / max-wait / hold music.
func LoadTeamConfig(ctx context.Context, d *db.DB, q *queries.Queries, tenantID uuid.UUID, team string) (TeamConfig, bool, error) {
	var (
		cfg       TeamConfig
		holdMusic string // the tenant's system hold music (subselected by get-team-moh)
	)
	err := d.Pool.QueryRow(ctx, q.GetTeamMOH, tenantID, team).Scan(
		&cfg.Strategy, &cfg.RingSeconds, &cfg.MaxWait, &cfg.MOH, &holdMusic)
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamConfig{}, false, nil
	}
	if err != nil {
		return TeamConfig{}, false, err
	}
	cfg.Name = team
	cfg.MOH = EffectiveMOH(cfg.MOH, holdMusic)
	return cfg, true, nil
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

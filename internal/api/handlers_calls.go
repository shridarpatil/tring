package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whatomate/tring/internal/freeswitch"
	"github.com/whatomate/tring/internal/provider"
	"github.com/whatomate/tring/internal/webhook"
)

type Call struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	Direction     string     `json:"direction"`
	Status        string     `json:"status"`
	FromNumber    string     `json:"from_number"`
	ToNumber      string     `json:"to_number"`
	AgentID       *uuid.UUID `json:"agent_id,omitempty"`
	FSUUID        string     `json:"fs_uuid"`
	StartedAt     time.Time  `json:"started_at"`
	AnsweredAt    *time.Time `json:"answered_at,omitempty"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	DurationSec   *int       `json:"duration_seconds,omitempty"`
	HangupCause   *string    `json:"hangup_cause,omitempty"`
	RecordingURL  *string    `json:"recording_url,omitempty"`
}

type createCallRequest struct {
	AgentID uuid.UUID `json:"agent_id"`
	To      string    `json:"to"`
}

// handleCreateCall is the tenant-scoped originate. The CRM's backend uses
// this with its API key, specifying which agent to ring as part of the
// request body.
func (s *Server) handleCreateCall(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AgentID == uuid.Nil || req.To == "" {
		writeError(w, http.StatusBadRequest, "agent_id and to are required")
		return
	}
	c, code, err := s.originateForAgent(r.Context(), t, req.AgentID, req.To)
	if err != nil {
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// handleSDKCreateCall is the JWT-scoped originate. The browser SDK uses this
// directly without ever holding the tenant API key — the agent identity comes
// from the JWT subject, the request body just carries the destination.
func (s *Server) handleSDKCreateCall(w http.ResponseWriter, r *http.Request) {
	claims := agentClaimsFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "missing agent claims")
		return
	}
	agentID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad subject")
		return
	}

	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.To == "" {
		writeError(w, http.StatusBadRequest, "to is required")
		return
	}

	// Look up the tenant the JWT was issued under. We trust the JWT-bound
	// tenant_id rather than refetching by sip_domain.
	t := &Tenant{ID: claims.TenantID, SipDomain: claims.SipDomain}

	c, code, err := s.originateForAgent(r.Context(), t, agentID, body.To)
	if err != nil {
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// originateForAgent is the shared originate path used by both the tenant-
// scoped and JWT-scoped handlers. Returns (call, http_status_on_error, err).
func (s *Server) originateForAgent(
	ctx context.Context,
	t *Tenant,
	agentID uuid.UUID,
	to string,
) (Call, int, error) {
	var sipUsername string
	err := s.db.Pool.QueryRow(ctx,
		`SELECT sip_username FROM agents WHERE id = $1 AND tenant_id = $2`,
		agentID, t.ID,
	).Scan(&sipUsername)
	if errors.Is(err, pgx.ErrNoRows) {
		return Call{}, http.StatusNotFound, errors.New("agent not found")
	}
	if err != nil {
		return Call{}, http.StatusInternalServerError, errors.New("agent lookup failed")
	}

	// Pick the trunk(s) to use based on the tenant's outbound strategy.
	// May return zero trunks (dev: fall back to loopback) or many
	// (failover: pipe-join in priority order).
	trunks, err := s.selectTrunks(ctx, t)
	if err != nil {
		return Call{}, http.StatusBadRequest, err
	}

	// Normalize using the primary trunk's provider. All trunks for a
	// tenant target the same country today (all four pre-baked ones are
	// India). When per-tenant cross-country trunks land in Phase 2, this
	// becomes a per-trunk loop.
	primaryProvider := provider.For("loopback")
	if len(trunks) > 0 {
		primaryProvider = provider.For(trunks[0].Provider)
	}
	normalized, err := primaryProvider.NormalizeE164(to)
	if err != nil {
		return Call{}, http.StatusBadRequest, fmt.Errorf("invalid destination %q: %w", to, err)
	}
	to = normalized

	fsUUID := uuid.New()

	sipDomain := t.SipDomain
	if sipDomain == "" {
		_ = s.db.Pool.QueryRow(ctx,
			`SELECT sip_domain FROM tenants WHERE id = $1`, t.ID,
		).Scan(&sipDomain)
	}
	aLeg := fmt.Sprintf("user/%s@%s", sipUsername, sipDomain)

	// Build BLegs. Each entry presents that trunk's own DID as caller-ID
	// via per-leg `[var=val]` prefix, so a roll-over from Airtel→Jio
	// doesn't try to present Airtel's DID over the Jio trunk (which
	// would fail caller-ID-validation at Jio's side).
	bLegs := make([]string, 0, max(1, len(trunks)))
	primaryDID := ""
	if len(trunks) == 0 {
		bLegs = append(bLegs, provider.For("loopback").DialString(to))
	} else {
		primaryDID = trunks[0].E164
		for _, tr := range trunks {
			p := provider.For(tr.Provider)
			bLegs = append(bLegs,
				fmt.Sprintf("[effective_caller_id_number=%s]%s", tr.E164, p.DialString(to)),
			)
		}
	}

	// If there are 2+ B-legs, set `continue_on_fail` per tenant config so
	// FS rolls to the next trunk only on the causes the operator opted in
	// for. Single-quote-wrapped so the commas inside don't terminate the
	// `{}` vars block.
	originateVars := map[string]string{}
	if len(bLegs) > 1 && len(t.FailoverCauses) > 0 {
		originateVars["continue_on_fail"] = "'" + strings.Join(t.FailoverCauses, ",") + "'"
	}

	or, err := s.fs.Originate(ctx, freeswitch.OriginateRequest{
		ALeg:     aLeg,
		BLegs:    bLegs,
		UUID:     fsUUID.String(),
		CallerID: primaryDID,
		Vars:     originateVars,
	})
	if err != nil {
		return Call{}, http.StatusBadGateway, fmt.Errorf("originate failed: %w", err)
	}

	// from_number for the CDR is the primary DID (the one we expected
	// the call to come from). If failover kicked in and we actually
	// used a secondary, the orchestrator will (Phase 3) record which
	// trunk-leg was answered.
	outboundE164 := primaryDID

	var c Call
	err = s.db.Pool.QueryRow(ctx, `
		INSERT INTO calls (tenant_id, direction, status, from_number, to_number, agent_id, fs_uuid, fs_job_uuid)
		VALUES ($1, 'outbound'::call_direction, 'ringing'::call_status, $2, $3, $4, $5, NULLIF($6,''))
		RETURNING id, tenant_id, direction::text, status::text, from_number, to_number, agent_id, fs_uuid, started_at
	`, t.ID, outboundE164, to, agentID, or.ChannelUUID, or.JobUUID).
		Scan(&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber, &c.AgentID, &c.FSUUID, &c.StartedAt)
	if err != nil {
		return Call{}, http.StatusInternalServerError, fmt.Errorf("insert call: %w", err)
	}

	// Fire call.ringing webhook async (best-effort; can't block originate
	// on third-party endpoint latency).
	s.notifier.Emit(ctx, t.ID, webhook.EventCallRinging, callPayload(c))
	return c, 0, nil
}

// callPayload is the data shape we ship in webhook envelopes. Mirrors the
// REST response so integrators can use one decoder for both surfaces.
func callPayload(c Call) map[string]interface{} {
	return map[string]interface{}{"call": c}
}

const (
	callsDefaultLimit = 50
	callsMaxLimit     = 200
)

const callSelectColumns = `
	id, tenant_id, direction::text, status::text, from_number, to_number,
	agent_id, fs_uuid, started_at, answered_at, ended_at,
	duration_seconds, hangup_cause, recording_url
`

func scanCall(scanner interface{ Scan(...interface{}) error }) (Call, error) {
	var c Call
	err := scanner.Scan(
		&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber,
		&c.AgentID, &c.FSUUID, &c.StartedAt, &c.AnsweredAt, &c.EndedAt,
		&c.DurationSec, &c.HangupCause, &c.RecordingURL,
	)
	return c, err
}

func (s *Server) handleGetCall(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row := s.db.Pool.QueryRow(r.Context(),
		`SELECT `+callSelectColumns+` FROM calls WHERE id = $1 AND tenant_id = $2`,
		id, t.ID,
	)
	c, err := scanCall(row)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleListCalls(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	limit := callsDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > callsMaxLimit {
		limit = callsMaxLimit
	}

	args := []interface{}{t.ID, limit}
	q := `
		SELECT ` + callSelectColumns + `
		FROM calls
		WHERE tenant_id = $1
	`
	if v := r.URL.Query().Get("status"); v != "" {
		q += ` AND status = $3::call_status`
		args = append(args, v)
	}
	q += ` ORDER BY started_at DESC LIMIT $2`

	rows, err := s.db.Pool.Query(r.Context(), q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	out := []Call{}
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

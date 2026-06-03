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
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	Direction    string     `json:"direction"`
	Status       string     `json:"status"`
	FromNumber   string     `json:"from_number"`
	ToNumber     string     `json:"to_number"`
	AgentID      *uuid.UUID `json:"agent_id,omitempty"`
	AgentName    *string    `json:"agent_name,omitempty"` // display_name|sip of the handling agent
	ClientRef    *string    `json:"client_ref,omitempty"` // embedder-supplied caller identity (vs guest_*)
	FSUUID       string     `json:"fs_uuid"`
	StartedAt    time.Time  `json:"started_at"`
	AnsweredAt   *time.Time `json:"answered_at,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DurationSec  *int       `json:"duration_seconds,omitempty"`
	HangupCause  *string    `json:"hangup_cause,omitempty"`
	RecordingURL *string    `json:"recording_url,omitempty"`
	// IVRPath is the recorded IVR journey for inbound/IVR calls — an ordered
	// JSON array of steps (node, DTMF pressed, transfer target, outcome).
	// Passed through verbatim from the calls.ivr_path JSONB column; empty
	// array for outbound calls. Shape: see orchestrator.SimStep / the
	// console's IvrStep type.
	IVRPath json.RawMessage `json:"ivr_path,omitempty"`
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
	err := s.db.Pool.QueryRow(ctx, s.q.GetAgentSipUsername,
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

	// Normalize using the primary trunk's provider_type. All trunks for
	// a tenant target the same country today; when cross-country trunks
	// land in Phase 2, this becomes a per-trunk loop.
	primaryProvider := provider.For("loopback")
	if len(trunks) > 0 {
		primaryProvider = provider.For(trunks[0].ProviderType)
	}
	normalized, err := primaryProvider.NormalizeE164(to)
	if err != nil {
		return Call{}, http.StatusBadRequest, fmt.Errorf("invalid destination %q: %w", to, err)
	}
	to = normalized

	fsUUID := uuid.New()

	sipDomain := t.SipDomain
	if sipDomain == "" {
		_ = s.db.Pool.QueryRow(ctx, s.q.GetTenantSipDomain, t.ID).Scan(&sipDomain)
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
			dial := fmt.Sprintf("sofia/gateway/%s/%s", tr.Gateway, to)
			bLegs = append(bLegs,
				fmt.Sprintf("[effective_caller_id_number=%s]%s", tr.E164, dial),
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
	err = s.db.Pool.QueryRow(ctx, s.q.CreateCallOutbound,
		t.ID, outboundE164, to, agentID, or.ChannelUUID, or.JobUUID).
		Scan(&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber, &c.AgentID, &c.FSUUID, &c.StartedAt)
	if err != nil {
		return Call{}, http.StatusInternalServerError, fmt.Errorf("insert call: %w", err)
	}

	// Fire call.ringing webhook async (best-effort; can't block originate
	// on third-party endpoint latency).
	s.notifier.Emit(ctx, t.ID, webhook.EventCallRinging, callPayload(c))
	return c, 0, nil
}

type connectCallRequest struct {
	AgentNumber  string `json:"agent_number"`
	ClientNumber string `json:"client_number"`
}

// handleConnectCall bridges two PSTN numbers over the tenant's trunk: it
// rings agent_number first and, on answer, bridges client_number. Both legs
// present the trunk's DID as caller ID (regulatory: you can only present a
// number you own). This is the click-to-call / callback pattern — neither
// party needs a softphone, just a phone that rings.
//
//	curl -X POST .../v1/calls/connect -H "Authorization: Bearer $KEY" \
//	  -d '{"agent_number":"+919876543210","client_number":"+919812345678"}'
func (s *Server) handleConnectCall(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req connectCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AgentNumber == "" || req.ClientNumber == "" {
		writeError(w, http.StatusBadRequest, "agent_number and client_number are required")
		return
	}
	c, code, err := s.originateBridge(r.Context(), t, req.AgentNumber, req.ClientNumber)
	if err != nil {
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// originateBridge rings agent_number and bridges client_number, both over the
// tenant's selected trunk. Returns (call, http_status_on_error, err).
func (s *Server) originateBridge(
	ctx context.Context,
	t *Tenant,
	agentNumber, clientNumber string,
) (Call, int, error) {
	trunks, err := s.selectTrunks(ctx, t)
	if err != nil {
		return Call{}, http.StatusBadRequest, err
	}

	// Normalize both numbers with the primary trunk's provider_type rules
	// (loopback passthrough in dev when no trunk is configured).
	primaryProvider := provider.For("loopback")
	if len(trunks) > 0 {
		primaryProvider = provider.For(trunks[0].ProviderType)
	}
	agentNorm, err := primaryProvider.NormalizeE164(agentNumber)
	if err != nil {
		return Call{}, http.StatusBadRequest, fmt.Errorf("invalid agent_number %q: %w", agentNumber, err)
	}
	clientNorm, err := primaryProvider.NormalizeE164(clientNumber)
	if err != nil {
		return Call{}, http.StatusBadRequest, fmt.Errorf("invalid client_number %q: %w", clientNumber, err)
	}

	aLeg, bLegs, callerID := buildBridgeLegs(trunks, agentNorm, clientNorm)

	fsUUID := uuid.New()
	or, err := s.fs.Originate(ctx, freeswitch.OriginateRequest{
		ALeg:     aLeg,
		BLegs:    bLegs,
		UUID:     fsUUID.String(),
		CallerID: callerID, // caller ID the agent's phone sees (the DID)
	})
	if err != nil {
		return Call{}, http.StatusBadGateway, fmt.Errorf("originate failed: %w", err)
	}

	// from = agent (the party rung first), to = client (the destination).
	// agent_id is NULL — this path bridges raw numbers, not a SIP agent.
	var c Call
	err = s.db.Pool.QueryRow(ctx, s.q.CreateCallBridge,
		t.ID, agentNorm, clientNorm, or.ChannelUUID, or.JobUUID).
		Scan(&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber, &c.AgentID, &c.FSUUID, &c.StartedAt)
	if err != nil {
		return Call{}, http.StatusInternalServerError, fmt.Errorf("insert call: %w", err)
	}

	s.notifier.Emit(ctx, t.ID, webhook.EventCallRinging, callPayload(c))
	return c, 0, nil
}

// buildBridgeLegs constructs the originate legs for a two-PSTN-number bridge.
// A-leg rings the agent's phone; on answer FS bridges to the client. Both
// present the trunk's DID as caller ID. With no trunk (dev), falls back to
// loopback dial strings and no caller ID. Pure function — unit-tested for the
// exact dial-string shape FreeSWITCH expects.
func buildBridgeLegs(trunks []trunk, agentNumber, clientNumber string) (aLeg string, bLegs []string, callerID string) {
	if len(trunks) == 0 {
		lp := provider.For("loopback")
		return lp.DialString(agentNumber), []string{lp.DialString(clientNumber)}, ""
	}
	primary := trunks[0]
	aLeg = fmt.Sprintf("sofia/gateway/%s/%s", primary.Gateway, agentNumber)
	bLegs = []string{
		fmt.Sprintf(
			"[effective_caller_id_number=%s]sofia/gateway/%s/%s",
			primary.E164, primary.Gateway, clientNumber,
		),
	}
	return aLeg, bLegs, primary.E164
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

func scanCall(scanner interface{ Scan(...interface{}) error }) (Call, error) {
	var c Call
	// Scan the jsonb ivr_path into a plain []byte, then hand it to the
	// RawMessage field — pgx returns jsonb as raw bytes and we pass them
	// through to the client unchanged.
	var ivrPath []byte
	err := scanner.Scan(
		&c.ID, &c.TenantID, &c.Direction, &c.Status, &c.FromNumber, &c.ToNumber,
		&c.AgentID, &c.FSUUID, &c.StartedAt, &c.AnsweredAt, &c.EndedAt,
		&c.DurationSec, &c.HangupCause, &c.RecordingURL, &ivrPath, &c.AgentName, &c.ClientRef,
	)
	if len(ivrPath) > 0 {
		c.IVRPath = ivrPath
	}
	return c, err
}

func (s *Server) handleGetCall(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row := s.db.Pool.QueryRow(r.Context(), s.q.GetCall, id, t.ID)
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

	// Two static queries instead of string-building: the status filter just
	// swaps which named query (and arg set) we use.
	query := s.q.ListCalls
	args := []interface{}{t.ID, limit}
	if v := r.URL.Query().Get("status"); v != "" {
		query = s.q.ListCallsByStatus
		args = append(args, v)
	}

	rows, err := s.db.Pool.Query(r.Context(), query, args...)
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

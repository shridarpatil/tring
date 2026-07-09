package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/presence"
	"github.com/shridarpatil/tring/internal/webhook"
)

// Ensure pgx errors are reachable in PATCH endpoints below.
var _ = errors.Is

// ---- Tenants ------------------------------------------------------------

func (s *Server) handleGetMyTenant(w http.ResponseWriter, r *http.Request) {
	// Copy the auth-resolved tenant and enrich with the settings not carried in
	// the auth context (hold music, record toggle) so the Settings page sees the
	// current selection.
	t := *tenantFromCtx(r.Context())
	_ = s.db.Pool.QueryRow(r.Context(), s.q.GetTenantSettings, t.ID).Scan(&t.HoldMusic, &t.RecordCalls, &t.MaxCallSeconds, &t.TransferFailedMessage)
	writeJSON(w, http.StatusOK, t)
}

// ---- Agents -------------------------------------------------------------

type Agent struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	SipUsername string    `json:"sip_username"`
	SipPassword string    `json:"sip_password,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	// AvailableForCalls is the backend-managed availability (the integrator's
	// Away/Break button). Separate from connection presence: the orchestrator
	// rings an agent only when they're BOTH connected AND available_for_calls.
	AvailableForCalls bool      `json:"available_for_calls"`
	CreatedAt         time.Time `json:"created_at"`
	// Status is the agent's live EFFECTIVE state, populated on list responses.
	// It combines Redis presence with available_for_calls: "on_call" (busy),
	// "away" (connected but marked unavailable), "online" (connected and
	// available), or "offline". Legacy SIP transport reports "online"/"offline"
	// from registration, or "unknown" if FreeSWITCH couldn't be reached.
	Status string `json:"status,omitempty"`
}

type createAgentRequest struct {
	SipUsername string `json:"sip_username"`
	DisplayName string `json:"display_name"`
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.SipUsername == "" {
		writeError(w, http.StatusBadRequest, "sip_username required")
		return
	}

	sipPassword, err := GenerateSipPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate password")
		return
	}

	var a Agent
	err = s.db.Pool.QueryRow(r.Context(), s.q.CreateAgent,
		t.ID, req.SipUsername, sipPassword, req.DisplayName).Scan(
		&a.ID, &a.TenantID, &a.SipUsername, &a.SipPassword, &a.DisplayName, &a.AvailableForCalls, &a.CreatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "sip_username already exists for this tenant")
			return
		}
		writeError(w, http.StatusInternalServerError, "create agent failed")
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// agentsList returns a tenant's agents enriched with live presence status. The
// list endpoint and the SSE snapshot share it. Needs the Tenant (SipDomain) for
// the legacy SIP-registration fallback.
func (s *Server) agentsList(ctx context.Context, t *Tenant) ([]Agent, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListAgents, t.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []Agent{}
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.TenantID, &a.SipUsername, &a.DisplayName, &a.AvailableForCalls, &a.CreatedAt); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	if s.agentTransport == "gateway" && s.presence.Enabled() {
		for i := range agents {
			st := s.presence.Status(ctx, t.ID.String(), agents[i].SipUsername)
			if st == presence.StatusOnline && !agents[i].AvailableForCalls {
				st = "away"
			}
			agents[i].Status = st
		}
	} else {
		regs, regErr := s.fs.RegisteredUsers(ctx)
		for i := range agents {
			switch {
			case regErr != nil:
				agents[i].Status = "unknown"
			case regs[freeswitch.RegKey(agents[i].SipUsername, t.SipDomain)]:
				agents[i].Status = "online"
			default:
				agents[i].Status = "offline"
			}
		}
	}
	return agents, nil
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	agents, err := s.agentsList(r.Context(), t)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

// resolveAgentRef resolves an agent path param that may be EITHER a UUID or a
// sip_username, so integrators can key agents by their own CRM user id (set as
// the sip_username) and skip a list→id lookup. Returns the agent's UUID id and
// sip_username; pgx.ErrNoRows when no agent matches (callers map it to 404).
func (s *Server) resolveAgentRef(ctx context.Context, tenantID uuid.UUID, ref string) (uuid.UUID, string, error) {
	if id, err := uuid.Parse(ref); err == nil {
		var sip string
		if err := s.db.Pool.QueryRow(ctx, s.q.GetAgentSipUsername, id, tenantID).Scan(&sip); err != nil {
			return uuid.Nil, "", err
		}
		return id, sip, nil
	}
	// Not a UUID → treat the param as a sip_username.
	var id uuid.UUID
	var label string
	if err := s.db.Pool.QueryRow(ctx, s.q.GetAgentBySIP, tenantID, ref).Scan(&id, &label); err != nil {
		return uuid.Nil, "", err
	}
	return id, ref, nil
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, _, err := s.resolveAgentRef(r.Context(), t.ID, chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	var a Agent
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAgent,
		id, t.ID).Scan(&a.ID, &a.TenantID, &a.SipUsername, &a.DisplayName, &a.AvailableForCalls, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// setAgentAvailabilityRequest is the body of POST /v1/agents/{id}/availability.
type setAgentAvailabilityRequest struct {
	Available bool `json:"available"`
}

// handleSetAgentAvailability flips an agent's available_for_calls (the
// integrator's Away/Break toggle, driven from THEIR backend with the tenant
// API key). Unavailable agents stay connected but the orchestrator stops
// ringing them. Emits an agent.status webhook so the CRM can track the change.
func (s *Server) handleSetAgentAvailability(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	// {id} accepts the agent UUID or its sip_username (= the integrator's CRM
	// user id), so the Away/Break toggle can be driven by that id directly.
	id, _, err := s.resolveAgentRef(r.Context(), t.ID, chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	var req setAgentAvailabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var a Agent
	err = s.db.Pool.QueryRow(r.Context(), s.q.SetAgentAvailability, req.Available, id, t.ID).Scan(
		&a.ID, &a.TenantID, &a.SipUsername, &a.DisplayName, &a.AvailableForCalls, &a.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	s.notifier.Emit(r.Context(), t.ID, webhook.EventAgentStatus, map[string]any{
		"agent_id":            a.ID,
		"sip_username":        a.SipUsername,
		"available_for_calls": a.AvailableForCalls,
	})
	s.events.Publish(r.Context(), t.ID.String(), events.TypeAgentStatus, map[string]any{
		"agent_id":            a.ID,
		"sip_username":        a.SipUsername,
		"available_for_calls": a.AvailableForCalls,
	})
	writeJSON(w, http.StatusOK, a)
}

// ---- Numbers ------------------------------------------------------------

type Number struct {
	ID       uuid.UUID  `json:"id"`
	TenantID uuid.UUID  `json:"tenant_id"`
	E164     string     `json:"e164"`
	Type     string     `json:"type"`
	TrunkID  *uuid.UUID `json:"trunk_id"`
	// FlowID points at the IVR flow run for inbound calls to this DID
	// (orchestrator's outbound-ESL listener looks it up on each call).
	// nil = no IVR — calls fall through to the default dialplan.
	FlowID        *uuid.UUID `json:"flow_id"`
	ProviderDIDID string     `json:"provider_did_id,omitempty"`
	Priority      int        `json:"priority"`
	Active        bool       `json:"active"`
	CreatedAt     time.Time  `json:"created_at"`
}

type createNumberRequest struct {
	E164          string     `json:"e164"`
	Type          string     `json:"type"`
	TrunkID       *uuid.UUID `json:"trunk_id"`
	FlowID        *uuid.UUID `json:"flow_id"`
	ProviderDIDID string     `json:"provider_did_id"`
	Priority      *int       `json:"priority,omitempty"`
}

func (s *Server) handleCreateNumber(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createNumberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.E164 == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "e164 and type are required")
		return
	}
	switch req.Type {
	case "inbound", "outbound", "masking":
	default:
		writeError(w, http.StatusBadRequest, "type must be inbound|outbound|masking")
		return
	}
	// Outbound numbers MUST reference a trunk so the call has somewhere
	// to land. Inbound/masking numbers can exist without one (the trunk
	// is implicit — whoever sent the inbound INVITE).
	if req.Type == "outbound" && req.TrunkID == nil {
		writeError(w, http.StatusBadRequest, "trunk_id is required for outbound numbers")
		return
	}

	priority := 100
	if req.Priority != nil {
		priority = *req.Priority
	}

	var n Number
	err := s.db.Pool.QueryRow(r.Context(), s.q.CreateNumber,
		t.ID, req.E164, req.Type, req.TrunkID, req.FlowID, req.ProviderDIDID, priority).Scan(
		&n.ID, &n.TenantID, &n.E164, &n.Type, &n.TrunkID, &n.FlowID, &n.ProviderDIDID, &n.Priority, &n.Active, &n.CreatedAt,
	)
	if err != nil {
		writeError(w, http.StatusConflict, "number already registered for this tenant: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

type patchNumberRequest struct {
	Priority *int  `json:"priority,omitempty"`
	Active   *bool `json:"active,omitempty"`
	// FlowID is a tri-state: absent = no change, present-non-nil = set,
	// present-and-null can't be expressed by *uuid.UUID alone, so we
	// also carry a `clear_flow` flag for the explicit-unassign case.
	FlowID    *uuid.UUID `json:"flow_id,omitempty"`
	ClearFlow bool       `json:"clear_flow,omitempty"`
}

// handlePatchNumber updates a number's priority / active flag. Used to
// flip a trunk on or off without deleting the row (preserves the DID
// allocation record for audit). Operators driving `manual` strategy use
// this to switch primary; failover/round_robin tenants use it to
// quarantine a misbehaving trunk.
//
// Guardrail: deactivating the *last* active outbound trunk would leave
// the tenant with zero usable trunks and silently break every outbound
// call. We reject that with 409. Operators must register a replacement
// first, or accept that no calls will work and explicitly DELETE the
// number (Phase 2 endpoint) instead.
func (s *Server) handlePatchNumber(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req patchNumberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Priority == nil && req.Active == nil && req.FlowID == nil && !req.ClearFlow {
		writeError(w, http.StatusBadRequest, "at least one of priority, active, flow_id, or clear_flow is required")
		return
	}

	// If the request would deactivate an outbound trunk, check this isn't
	// the only active outbound trunk left. Skip the check for inbound /
	// masking numbers (they don't carry outbound caller-ID and have
	// different invariants).
	if req.Active != nil && !*req.Active {
		var rowType string
		var rowActive bool
		err := s.db.Pool.QueryRow(r.Context(), s.q.GetNumberTypeActive,
			id, t.ID,
		).Scan(&rowType, &rowActive)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "number not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "lookup failed: "+err.Error())
			return
		}
		if rowType == "outbound" && rowActive {
			var otherActive int
			err = s.db.Pool.QueryRow(r.Context(), s.q.CountOtherActiveOutbound,
				t.ID, id).Scan(&otherActive)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "lookup failed: "+err.Error())
				return
			}
			if otherActive == 0 {
				writeError(w, http.StatusConflict,
					"cannot deactivate the last active outbound trunk for this tenant; "+
						"register another outbound number first")
				return
			}
		}
	}

	// flow_id update semantics:
	//   clear_flow=true   → SET flow_id = NULL  (unassign)
	//   flow_id present   → SET flow_id = req.FlowID
	//   neither           → keep existing
	var n Number
	err = s.db.Pool.QueryRow(r.Context(), s.q.UpdateNumber,
		req.Priority, req.Active, req.ClearFlow, req.FlowID, id, t.ID).Scan(
		&n.ID, &n.TenantID, &n.E164, &n.Type, &n.TrunkID, &n.FlowID, &n.ProviderDIDID, &n.Priority, &n.Active, &n.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "number not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type patchTenantRequest struct {
	OutboundStrategy *string  `json:"outbound_strategy,omitempty"`
	FailoverCauses   []string `json:"failover_causes,omitempty"`
	// HoldMusic: an audio asset's file_path to use as system hold music, or "" to
	// clear (revert to the built-in default). nil = leave unchanged.
	HoldMusic *string `json:"hold_music,omitempty"`
	// RecordCalls toggles call recording. nil = leave unchanged.
	RecordCalls *bool `json:"record_calls,omitempty"`
	// MaxCallSeconds caps call duration (and sets the TURN credential lifetime).
	// nil = leave unchanged.
	MaxCallSeconds *int `json:"max_call_seconds,omitempty"`
	// TransferFailedMessage sets the blind-transfer no-answer copy. nil = unchanged.
	TransferFailedMessage *string `json:"transfer_failed_message,omitempty"`
}

// handlePatchMyTenant updates tenant-level settings. Today: outbound
// trunk strategy and the cause list that triggers fail-over.
func (s *Server) handlePatchMyTenant(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req patchTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.OutboundStrategy == nil && req.FailoverCauses == nil && req.HoldMusic == nil && req.RecordCalls == nil && req.MaxCallSeconds == nil && req.TransferFailedMessage == nil {
		writeError(w, http.StatusBadRequest, "at least one of outbound_strategy, failover_causes, hold_music, record_calls, max_call_seconds or transfer_failed_message is required")
		return
	}
	if req.MaxCallSeconds != nil && (*req.MaxCallSeconds < 60 || *req.MaxCallSeconds > 86400) {
		writeError(w, http.StatusBadRequest, "max_call_seconds must be between 60 and 86400 (1 min–24 h)")
		return
	}
	if req.OutboundStrategy != nil {
		switch *req.OutboundStrategy {
		case "failover", "round_robin", "manual", "load_based":
		default:
			writeError(w, http.StatusBadRequest, "outbound_strategy must be failover|round_robin|manual|load_based")
			return
		}
	}
	var causes interface{}
	if req.FailoverCauses != nil {
		causes = req.FailoverCauses
	}

	// Pre-render the no-answer message to Opus so the gateway plays it as a file
	// (same pipeline as flow prompts). Best-effort: on any failure store "" audio
	// → the gateway degrades to silent-then-hangup, exactly like a silent text node.
	// nil message = leave audio unchanged; any SET message re-derives the audio
	// (synth when possible, else "") so a stale synthesized prompt never plays.
	var xferAudio *string
	if req.TransferFailedMessage != nil {
		msg := strings.TrimSpace(*req.TransferFailedMessage)
		if msg == "" {
			empty := ""
			xferAudio = &empty
		} else if s.tts != nil && s.tts.Enabled() {
			if fp, err := s.synthFlowText(r.Context(), msg); err != nil {
				s.log.Warn("tts: transfer message synth failed; message will be silent", "err", err)
				empty := ""
				xferAudio = &empty
			} else {
				xferAudio = &fp
			}
		} else {
			// Message set but TTS unavailable: clear any prior synthesized audio so
			// an edited message never plays a stale prompt (degrades to silent).
			empty := ""
			xferAudio = &empty
		}
	}

	var out Tenant
	err := s.db.Pool.QueryRow(r.Context(), s.q.UpdateTenant,
		req.OutboundStrategy, causes, t.ID, req.HoldMusic, req.RecordCalls, req.MaxCallSeconds,
		req.TransferFailedMessage, xferAudio).
		Scan(&out.ID, &out.Name, &out.SipDomain, &out.OutboundStrategy, &out.FailoverCauses, &out.HoldMusic, &out.RecordCalls, &out.MaxCallSeconds, &out.TransferFailedMessage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListNumbers(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListNumbers, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	numbers := []Number{}
	for rows.Next() {
		var n Number
		if err := rows.Scan(&n.ID, &n.TenantID, &n.E164, &n.Type, &n.TrunkID, &n.FlowID, &n.ProviderDIDID, &n.Priority, &n.Active, &n.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		numbers = append(numbers, n)
	}
	writeJSON(w, http.StatusOK, numbers)
}

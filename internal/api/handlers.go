package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Ensure pgx errors are reachable in PATCH endpoints below.
var _ = errors.Is

// ---- Tenants ------------------------------------------------------------

func (s *Server) handleGetMyTenant(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, tenantFromCtx(r.Context()))
}

// ---- Agents -------------------------------------------------------------

type Agent struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	SipUsername string    `json:"sip_username"`
	SipPassword string    `json:"sip_password,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
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
	err = s.db.Pool.QueryRow(r.Context(), `
		INSERT INTO agents (tenant_id, sip_username, sip_password, display_name)
		VALUES ($1, $2, $3, NULLIF($4, ''))
		RETURNING id, tenant_id, sip_username, sip_password, COALESCE(display_name, ''), created_at
	`, t.ID, req.SipUsername, sipPassword, req.DisplayName).Scan(
		&a.ID, &a.TenantID, &a.SipUsername, &a.SipPassword, &a.DisplayName, &a.CreatedAt,
	)
	if err != nil {
		writeError(w, http.StatusConflict, "sip_username already exists for this tenant")
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), `
		SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), created_at
		FROM agents
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	agents := []Agent{}
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.TenantID, &a.SipUsername, &a.DisplayName, &a.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		agents = append(agents, a)
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var a Agent
	err = s.db.Pool.QueryRow(r.Context(), `
		SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), created_at
		FROM agents
		WHERE id = $1 AND tenant_id = $2
	`, id, t.ID).Scan(&a.ID, &a.TenantID, &a.SipUsername, &a.DisplayName, &a.CreatedAt)
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

// ---- Numbers ------------------------------------------------------------

type Number struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	E164          string     `json:"e164"`
	Type          string     `json:"type"`
	TrunkID       *uuid.UUID `json:"trunk_id"`
	ProviderDIDID string     `json:"provider_did_id,omitempty"`
	Priority      int        `json:"priority"`
	Active        bool       `json:"active"`
	CreatedAt     time.Time  `json:"created_at"`
}

type createNumberRequest struct {
	E164          string     `json:"e164"`
	Type          string     `json:"type"`
	TrunkID       *uuid.UUID `json:"trunk_id"`
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
	err := s.db.Pool.QueryRow(r.Context(), `
		INSERT INTO numbers (tenant_id, e164, type, trunk_id, provider_did_id, priority)
		VALUES ($1, $2, $3::number_type, $4, NULLIF($5, ''), $6)
		RETURNING id, tenant_id, e164, type::text, trunk_id, COALESCE(provider_did_id, ''), priority, active, created_at
	`, t.ID, req.E164, req.Type, req.TrunkID, req.ProviderDIDID, priority).Scan(
		&n.ID, &n.TenantID, &n.E164, &n.Type, &n.TrunkID, &n.ProviderDIDID, &n.Priority, &n.Active, &n.CreatedAt,
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
	if req.Priority == nil && req.Active == nil {
		writeError(w, http.StatusBadRequest, "at least one of priority or active is required")
		return
	}

	// If the request would deactivate an outbound trunk, check this isn't
	// the only active outbound trunk left. Skip the check for inbound /
	// masking numbers (they don't carry outbound caller-ID and have
	// different invariants).
	if req.Active != nil && !*req.Active {
		var rowType string
		var rowActive bool
		err := s.db.Pool.QueryRow(r.Context(),
			`SELECT type::text, active FROM numbers WHERE id = $1 AND tenant_id = $2`,
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
			err = s.db.Pool.QueryRow(r.Context(), `
				SELECT COUNT(*) FROM numbers
				WHERE tenant_id = $1 AND type = 'outbound' AND active AND id != $2
			`, t.ID, id).Scan(&otherActive)
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

	var n Number
	err = s.db.Pool.QueryRow(r.Context(), `
		UPDATE numbers
		SET priority = COALESCE($1, priority),
		    active   = COALESCE($2, active)
		WHERE id = $3 AND tenant_id = $4
		RETURNING id, tenant_id, e164, type::text, trunk_id, COALESCE(provider_did_id, ''), priority, active, created_at
	`, req.Priority, req.Active, id, t.ID).Scan(
		&n.ID, &n.TenantID, &n.E164, &n.Type, &n.TrunkID, &n.ProviderDIDID, &n.Priority, &n.Active, &n.CreatedAt,
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
	if req.OutboundStrategy == nil && req.FailoverCauses == nil {
		writeError(w, http.StatusBadRequest, "at least one of outbound_strategy or failover_causes is required")
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

	var out Tenant
	err := s.db.Pool.QueryRow(r.Context(), `
		UPDATE tenants
		SET outbound_strategy = COALESCE($1, outbound_strategy),
		    failover_causes   = COALESCE($2::TEXT[], failover_causes)
		WHERE id = $3
		RETURNING id, name, sip_domain, outbound_strategy, failover_causes
	`, req.OutboundStrategy, causes, t.ID).
		Scan(&out.ID, &out.Name, &out.SipDomain, &out.OutboundStrategy, &out.FailoverCauses)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListNumbers(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), `
		SELECT id, tenant_id, e164, type::text, trunk_id, COALESCE(provider_did_id, ''), priority, active, created_at
		FROM numbers
		WHERE tenant_id = $1
		ORDER BY type, priority ASC, created_at DESC
	`, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	numbers := []Number{}
	for rows.Next() {
		var n Number
		if err := rows.Scan(&n.ID, &n.TenantID, &n.E164, &n.Type, &n.TrunkID, &n.ProviderDIDID, &n.Priority, &n.Active, &n.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		numbers = append(numbers, n)
	}
	writeJSON(w, http.StatusOK, numbers)
}

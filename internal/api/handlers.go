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
	ID            uuid.UUID `json:"id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	E164          string    `json:"e164"`
	Type          string    `json:"type"`
	Provider      string    `json:"provider"`
	ProviderDIDID string    `json:"provider_did_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type createNumberRequest struct {
	E164          string `json:"e164"`
	Type          string `json:"type"`
	Provider      string `json:"provider"`
	ProviderDIDID string `json:"provider_did_id"`
}

func (s *Server) handleCreateNumber(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createNumberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.E164 == "" || req.Type == "" || req.Provider == "" {
		writeError(w, http.StatusBadRequest, "e164, type, provider required")
		return
	}
	switch req.Type {
	case "inbound", "outbound", "masking":
	default:
		writeError(w, http.StatusBadRequest, "type must be inbound|outbound|masking")
		return
	}

	var n Number
	err := s.db.Pool.QueryRow(r.Context(), `
		INSERT INTO numbers (tenant_id, e164, type, provider, provider_did_id)
		VALUES ($1, $2, $3::number_type, $4, NULLIF($5, ''))
		RETURNING id, tenant_id, e164, type::text, provider, COALESCE(provider_did_id, ''), created_at
	`, t.ID, req.E164, req.Type, req.Provider, req.ProviderDIDID).Scan(
		&n.ID, &n.TenantID, &n.E164, &n.Type, &n.Provider, &n.ProviderDIDID, &n.CreatedAt,
	)
	if err != nil {
		writeError(w, http.StatusConflict, "number already registered for this tenant")
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) handleListNumbers(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), `
		SELECT id, tenant_id, e164, type::text, provider, COALESCE(provider_did_id, ''), created_at
		FROM numbers
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	numbers := []Number{}
	for rows.Next() {
		var n Number
		if err := rows.Scan(&n.ID, &n.TenantID, &n.E164, &n.Type, &n.Provider, &n.ProviderDIDID, &n.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		numbers = append(numbers, n)
	}
	writeJSON(w, http.StatusOK, numbers)
}

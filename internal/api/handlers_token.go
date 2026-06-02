package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const agentTokenTTL = 5 * time.Minute

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleAgentToken mints a short-lived JWT for a specific agent. Auth: tenant
// API key. The integrating CRM's backend calls this and ships the resulting
// token to the agent's browser, where the JS SDK uses it to fetch SIP
// credentials and never sees the tenant API key directly.
func (s *Server) handleAgentToken(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var sipUsername string
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAgentSipUsername,
		id, t.ID,
	).Scan(&sipUsername)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	tok, exp, err := s.IssueAgentToken(id, t.ID, sipUsername, t.SipDomain, agentTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue token")
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{Token: tok, ExpiresAt: exp})
}

type sipCreds struct {
	SipUsername string      `json:"sip_username"`
	SipPassword string      `json:"sip_password"`
	SipDomain   string      `json:"sip_domain"`
	WSURL       string      `json:"ws_url"`
	ICEServers  []iceServer `json:"ice_servers"`
}

// handleSDKCredentials returns the SIP credentials the JS SDK needs to
// register sip.js. Auth: agent JWT. Scope: the credentials belong to the
// JWT's subject agent — there is no way to fetch another agent's creds.
func (s *Server) handleSDKCredentials(w http.ResponseWriter, r *http.Request) {
	c := agentClaimsFromCtx(r.Context())
	if c == nil {
		writeError(w, http.StatusUnauthorized, "missing agent claims")
		return
	}
	agentID, err := uuid.Parse(c.Subject)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad subject")
		return
	}

	var creds sipCreds
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAgentSipCreds,
		agentID, c.TenantID).Scan(&creds.SipUsername, &creds.SipPassword, &creds.SipDomain)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	creds.WSURL = s.wsURL
	creds.ICEServers = s.iceServers
	writeJSON(w, http.StatusOK, creds)
}

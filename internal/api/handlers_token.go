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
	// GatewayURL + ICEServers let the SDK connect the gateway WebSocket from a
	// single token fetch — the integrator's token endpoint passes these through,
	// so the agent <script> tag needs only data-token-url (no data-gateway-url).
	GatewayURL string      `json:"gateway_url,omitempty"`
	ICEServers []iceServer `json:"ice_servers,omitempty"`
}

// handleAgentToken mints a short-lived JWT for a specific agent. Auth: tenant
// API key. The integrating CRM's backend calls this and ships the resulting
// token to the agent's browser, where the JS SDK uses it to authenticate the
// media-gateway WebSocket (/agent/ws) — the browser never sees the tenant API
// key directly.
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
	writeJSON(w, http.StatusOK, tokenResponse{
		Token:      tok,
		ExpiresAt:  exp,
		GatewayURL: s.gatewayPublicURL,
		ICEServers: s.iceServers,
	})
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// gatewayCallTokenTTL bounds how long a minted gateway call token is valid —
// long enough for the visitor's browser to set up the WebRTC call, short
// enough that a leaked token expires fast. The gateway consumes it at offer
// time; an established call continues past expiry.
const gatewayCallTokenTTL = 2 * time.Minute

// authGuestCredentialsRequest is the body for the API-key-authenticated mint
// (POST /v1/guest-credentials). client_ref is TRUSTED here because the caller
// proved tenant ownership with the API key — unlike the public browser mint,
// which never accepts it (a browser-supplied identity would be spoofable).
type authGuestCredentialsRequest struct {
	FlowID    string `json:"flow_id"`    // optional; falls back to the tenant default_flow_id
	ClientRef string `json:"client_ref"` // the verified caller identity, e.g. a CRM client id
}

// guestCredentialsResponse gives a visitor's browser everything it needs to
// place the call straight at the media gateway (ADR-001): the gateway URL, the
// pinned flow, a short-lived call token, and ICE servers. No SIP.
type guestCredentialsResponse struct {
	GatewayURL string      `json:"gateway_url"`
	FlowID     string      `json:"flow_id"`
	Token      string      `json:"token"`
	ICEServers []iceServer `json:"ice_servers"`
}

// NOTE: the public, unauthenticated browser mint was removed — every visitor
// token is now minted server-side via handleAuthGuestCredentials (tenant API
// key). Anonymous guests just get a token with no client_ref; the integrator
// gates that endpoint (CAPTCHA/rate-limit) before issuing.

// handleAuthGuestCredentials mints guest call credentials for an
// API-key-authenticated caller (the integrator's backend), attaching a
// TRUSTED client_ref — the verified identity of the logged-in caller. Because
// the request proved tenant ownership (AuthRequired), client_ref is safe to
// attribute on the call log, unlike the public browser mint.
//
// Flow for a logged-in support portal: portal backend (holds the API key)
// calls this with the session's client id → returns SIP creds to its own
// frontend → the browser uses them with the SDK. Mirrors the agent widget's
// data-token-url pattern.
func (s *Server) handleAuthGuestCredentials(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req authGuestCredentialsRequest
	// Body is optional (flow_id + client_ref); ignore decode errors on empty.
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Fallback flow comes from the tenant's configured default flow (used only
	// when the caller doesn't pass flow_id).
	var defaultFlow *uuid.UUID
	if err := s.db.Pool.QueryRow(r.Context(), s.q.GetGuestConfig, t.ID).Scan(&defaultFlow); err != nil {
		writeError(w, http.StatusInternalServerError, "tenant config lookup failed")
		return
	}
	s.mintGatewayCall(w, r, t.ID, defaultFlow, req.FlowID, req.ClientRef)
}

// mintGatewayCall is the shared core: resolve the flow and issue a short-lived
// gateway call token the visitor's browser presents to the media gateway. Both
// the public and the API-key mints call it; only the latter passes a non-empty
// clientRef (the trusted, attributable caller identity). No SIP session — the
// gateway dials FreeSWITCH itself (ADR-001).
func (s *Server) mintGatewayCall(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, defaultFlow *uuid.UUID, requestedFlow, clientRef string) {
	flowID, err := s.resolveGuestFlow(r.Context(), tenantID, requestedFlow, defaultFlow)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	token, _, err := s.IssueGatewayCallToken(tenantID, flowID.String(), clientRef, gatewayCallTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}

	writeJSON(w, http.StatusOK, guestCredentialsResponse{
		GatewayURL: s.gatewayPublicURL,
		FlowID:     flowID.String(),
		Token:      token,
		ICEServers: s.iceServersFor(clientRef, s.turnTTLFor(r.Context(), tenantID)),
	})
}

// resolveGuestFlow picks the flow a guest call lands in: the caller's
// requested flow_id if supplied (validated to belong to the tenant and be
// active), otherwise the tenant's configured default_flow_id. Errors if
// neither yields a usable flow — a guest with no flow has nothing to call.
func (s *Server) resolveGuestFlow(ctx context.Context, tenantID uuid.UUID, requested string, fallback *uuid.UUID) (uuid.UUID, error) {
	if requested != "" {
		fid, err := uuid.Parse(requested)
		if err != nil {
			return uuid.Nil, errors.New("invalid flow_id")
		}
		if err := s.assertFlowUsable(ctx, fid, tenantID); err != nil {
			return uuid.Nil, err
		}
		return fid, nil
	}
	if fallback != nil {
		return *fallback, nil
	}
	return uuid.Nil, errors.New("no flow for this guest call — pass flow_id, or set a default flow in Settings")
}

// assertFlowUsable confirms a flow exists, belongs to the tenant, and is
// active. Returns a caller-safe error otherwise.
func (s *Server) assertFlowUsable(ctx context.Context, flowID, tenantID uuid.UUID) error {
	var ok bool
	err := s.db.Pool.QueryRow(ctx, s.q.FlowUsable,
		flowID, tenantID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("flow not found or inactive")
	}
	if err != nil {
		return errors.New("flow lookup failed")
	}
	return nil
}

// ---- Guest widget config (tenant-scoped) --------------------------------

type guestConfigResponse struct {
	// DefaultFlowID: the tenant's default IVR flow for guest calls — the
	// fallback used only when a mint doesn't pass an explicit flow_id.
	DefaultFlowID *uuid.UUID `json:"default_flow_id"`
}

// handleGetGuestConfig returns the tenant's default guest flow. The console
// reads it to render the default-flow picker. (The old public_key /
// allowed_origins / guest_require_backend_mint fields went away with the public
// mint — all visitor tokens are now minted server-side via /v1/guest-credentials.)
func (s *Server) handleGetGuestConfig(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var resp guestConfigResponse
	if err := s.db.Pool.QueryRow(r.Context(), s.q.GetGuestConfig, t.ID).Scan(&resp.DefaultFlowID); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type patchGuestConfigRequest struct {
	// DefaultFlowID: a flow UUID to use as the tenant default, or "" to clear it.
	DefaultFlowID *string `json:"default_flow_id"`
}

// handlePatchGuestConfig sets the tenant's default guest flow.
func (s *Server) handlePatchGuestConfig(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req patchGuestConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.DefaultFlowID == nil {
		writeError(w, http.StatusBadRequest, "default_flow_id required")
		return
	}

	var flowID *uuid.UUID
	if *req.DefaultFlowID != "" {
		fid, err := uuid.Parse(*req.DefaultFlowID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid default_flow_id")
			return
		}
		if err := s.assertFlowUsable(r.Context(), fid, t.ID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		flowID = &fid
	}
	if _, err := s.db.Pool.Exec(r.Context(), s.q.UpdateDefaultFlow, flowID, t.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	s.handleGetGuestConfig(w, r)
}

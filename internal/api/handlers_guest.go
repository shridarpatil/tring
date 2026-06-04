package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	FlowID    string `json:"flow_id"`    // optional; falls back to tenant.support_flow_id
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

	// Fallback flow comes from the tenant's configured support flow. (The
	// require-backend flag doesn't gate this path — it IS the backend mint.)
	var (
		publicKey      string
		supportFlow    *uuid.UUID
		origins        []string
		requireBackend bool
	)
	if err := s.db.Pool.QueryRow(r.Context(), s.q.GetGuestConfig, t.ID).
		Scan(&publicKey, &supportFlow, &origins, &requireBackend); err != nil {
		writeError(w, http.StatusInternalServerError, "tenant config lookup failed")
		return
	}
	s.mintGatewayCall(w, r, t.ID, supportFlow, req.FlowID, req.ClientRef)
}

// mintGatewayCall is the shared core: resolve the flow and issue a short-lived
// gateway call token the visitor's browser presents to the media gateway. Both
// the public and the API-key mints call it; only the latter passes a non-empty
// clientRef (the trusted, attributable caller identity). No SIP session — the
// gateway dials FreeSWITCH itself (ADR-001).
func (s *Server) mintGatewayCall(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, supportFlow *uuid.UUID, requestedFlow, clientRef string) {
	flowID, err := s.resolveGuestFlow(r.Context(), tenantID, requestedFlow, supportFlow)
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
		ICEServers: s.iceServers,
	})
}

// resolveGuestFlow picks the flow a guest call lands in: the caller's
// requested flow_id if supplied (validated to belong to the tenant and be
// active), otherwise the tenant's configured support_flow_id. Errors if
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
	return uuid.Nil, errors.New("guest calling is not enabled for this tenant — an operator must set a support flow in Settings")
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
	PublicKey      string     `json:"public_key"`
	SupportFlowID  *uuid.UUID `json:"support_flow_id"`
	AllowedOrigins []string   `json:"allowed_origins"`
	// RequireBackendMint: when true the public mint is disabled and guest
	// credentials must be minted server-side via the API-key endpoint.
	RequireBackendMint bool `json:"guest_require_backend_mint"`
}

// handleGetGuestConfig returns the tenant's public widget key, the flow
// guest web-calls are routed into, and the Origin allowlist. The console
// reads this to render the embed snippet, support-flow picker, and the
// allowed-domains field.
func (s *Server) handleGetGuestConfig(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var resp guestConfigResponse
	err := s.db.Pool.QueryRow(r.Context(), s.q.GetGuestConfig,
		t.ID).Scan(&resp.PublicKey, &resp.SupportFlowID, &resp.AllowedOrigins, &resp.RequireBackendMint)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type patchGuestConfigRequest struct {
	// SupportFlowID: a flow UUID to pin guest calls to, or "" to clear it.
	SupportFlowID *string `json:"support_flow_id"`
	// AllowedOrigins: the domains the widget may be embedded on. Required
	// (non-empty) before guest minting works — fail-closed.
	AllowedOrigins *[]string `json:"allowed_origins"`
	// RequireBackendMint: true disables the public mint (backend API-key mint
	// only). nil = leave unchanged.
	RequireBackendMint *bool `json:"guest_require_backend_mint"`
}

// handlePatchGuestConfig sets the support flow and/or the Origin allowlist.
func (s *Server) handlePatchGuestConfig(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req patchGuestConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.SupportFlowID == nil && req.AllowedOrigins == nil && req.RequireBackendMint == nil {
		writeError(w, http.StatusBadRequest, "support_flow_id, allowed_origins, or guest_require_backend_mint required")
		return
	}

	if req.SupportFlowID != nil {
		var flowID *uuid.UUID
		if *req.SupportFlowID != "" {
			fid, err := uuid.Parse(*req.SupportFlowID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid support_flow_id")
				return
			}
			if err := s.assertFlowUsable(r.Context(), fid, t.ID); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			flowID = &fid
		}
		if _, err := s.db.Pool.Exec(r.Context(), s.q.UpdateSupportFlow, flowID, t.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
	}

	if req.AllowedOrigins != nil {
		normalized := make([]string, 0, len(*req.AllowedOrigins))
		for _, o := range *req.AllowedOrigins {
			n, err := normalizeOrigin(o)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			normalized = append(normalized, n)
		}
		if _, err := s.db.Pool.Exec(r.Context(), s.q.UpdateAllowedOrigins, normalized, t.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
	}

	if req.RequireBackendMint != nil {
		if _, err := s.db.Pool.Exec(r.Context(), s.q.UpdateGuestRequireBackendMint, *req.RequireBackendMint, t.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
	}

	s.handleGetGuestConfig(w, r)
}

// normalizeOrigin validates and canonicalizes an allowlist entry to the
// scheme://host[:port] form browsers send in the Origin header (no path,
// no trailing slash). Returns an error for anything that isn't a plain
// http(s) origin.
func normalizeOrigin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("invalid origin %q", s)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		(u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("origin %q must be scheme://host[:port] with no path", s)
	}
	return u.Scheme + "://" + u.Host, nil
}

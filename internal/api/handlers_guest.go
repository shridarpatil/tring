package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// guestSessionTTL bounds how long an ephemeral guest SIP identity is valid.
// Long enough for a customer to navigate an IVR and hold a call; short
// enough that a leaked credential expires fast. An in-progress call
// continues past expiry (FreeSWITCH already authed the channel) — the TTL
// only gates *new* registration / directory-auth lookups.
const guestSessionTTL = 15 * time.Minute

// maxConcurrentGuestSessions caps live (unexpired) guest sessions per tenant.
// The non-spoofable backstop against minting floods exhausting DB rows or
// FreeSWITCH channels. Generous for legit traffic, tight against abuse.
const maxConcurrentGuestSessions = 100

// guestMintRatePerMin is the per-IP request cap on the public mint endpoint.
const guestMintRatePerMin = 10

// guestDialTarget is the SIP user the guest SDK sends its INVITE to. The
// value is irrelevant to routing — the dialplan routes any user_class=guest
// call to the IVR regardless — but a stable, obviously-non-numeric target
// keeps logs readable and avoids colliding with the numeric-DID tring_ivr
// extension.
const guestDialTarget = "ivr"

type guestCredentialsRequest struct {
	PublicKey string `json:"public_key"`
	FlowID    string `json:"flow_id"` // optional; falls back to tenant.support_flow_id
}

// authGuestCredentialsRequest is the body for the API-key-authenticated mint
// (POST /v1/guest-credentials). client_ref is TRUSTED here because the caller
// proved tenant ownership with the API key — unlike the public browser mint,
// which never accepts it (a browser-supplied identity would be spoofable).
type authGuestCredentialsRequest struct {
	FlowID    string `json:"flow_id"`   // optional; falls back to tenant.support_flow_id
	ClientRef string `json:"client_ref"` // the verified caller identity, e.g. a CRM client id
}

type guestCredentialsResponse struct {
	SipUsername string      `json:"sip_username"`
	SipPassword string      `json:"sip_password"`
	SipDomain   string      `json:"sip_domain"`
	WSURL       string      `json:"ws_url"`
	ICEServers  []iceServer `json:"ice_servers"`
	DialTarget  string      `json:"dial_target"`
}

// handleGuestCredentials mints a short-lived, anonymous SIP identity for a
// website visitor so they can place a WebRTC call into a tenant's IVR flow.
// Public endpoint (no auth wall): the only credential is the tenant's
// *public* widget key, which is safe to embed in a web page.
//
// The minted identity is hard-pinned to one flow and — via the
// user_class=guest directory variable + dialplan branch — can never bridge
// to a trunk. See freeswitch/conf/dialplan/default.xml (tring_guest_ivr)
// and handleFSDirectory's guest branch.
func (s *Server) handleGuestCredentials(w http.ResponseWriter, r *http.Request) {
	var req guestCredentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PublicKey == "" {
		writeError(w, http.StatusBadRequest, "public_key required")
		return
	}

	var (
		tenantID       uuid.UUID
		sipDomain      string
		supportFlow    *uuid.UUID
		allowedOrigins []string
		requireBackend bool
	)
	err := s.db.Pool.QueryRow(r.Context(), s.q.GetTenantByPublicKey,
		req.PublicKey).Scan(&tenantID, &sipDomain, &supportFlow, &allowedOrigins, &requireBackend)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "unknown public key")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	// Per-tenant policy: this tenant requires backend-minted (attributable)
	// guest credentials, so the public browser mint is disabled. The caller
	// must mint via the API-key endpoint (POST /v1/guest-credentials).
	if requireBackend {
		writeError(w, http.StatusForbidden, "public guest mint disabled for this tenant; mint server-side via /v1/guest-credentials")
		return
	}

	// Origin allowlist: bind the public key to the tenant's domains so a
	// stolen key can't be embedded elsewhere. Fail-closed — an empty list
	// (or a missing Origin) is rejected. Browser-enforced only; the rate
	// limit + concurrent cap below cover spoofed/non-browser clients.
	if !originAllowed(r.Header.Get("Origin"), allowedOrigins) {
		writeError(w, http.StatusForbidden, "origin not allowed")
		return
	}

	// Public path never trusts a caller-supplied identity — pass "" so a
	// browser can't forge client_ref. The authenticated mint below sets it.
	s.mintGuestSession(w, r, tenantID, sipDomain, supportFlow, req.FlowID, "")
}

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
	s.mintGuestSession(w, r, t.ID, t.SipDomain, supportFlow, req.FlowID, req.ClientRef)
}

// mintGuestSession is the shared core: resolve the flow, enforce the
// per-tenant concurrent cap, create the ephemeral session (optionally tagged
// with a trusted clientRef), and return SIP credentials. Both the public and
// the API-key mints call it; only the latter passes a non-empty clientRef.
func (s *Server) mintGuestSession(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, sipDomain string, supportFlow *uuid.UUID, requestedFlow, clientRef string) {
	flowID, err := s.resolveGuestFlow(r.Context(), tenantID, requestedFlow, supportFlow)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Reap expired sessions lazily so the table stays bounded without a
	// background worker. Cheap (indexed on expires_at) and best-effort.
	_, _ = s.db.Pool.Exec(r.Context(), s.q.DeleteExpiredGuestSessions)

	// Per-tenant concurrent-session cap — the non-spoofable backstop against
	// a flood exhausting DB rows / FreeSWITCH channels.
	var active int
	if err := s.db.Pool.QueryRow(r.Context(), s.q.CountActiveGuestSessions,
		tenantID,
	).Scan(&active); err != nil {
		writeError(w, http.StatusInternalServerError, "session count failed")
		return
	}
	if active >= maxConcurrentGuestSessions {
		writeError(w, http.StatusTooManyRequests, "too many active guest sessions")
		return
	}

	user := "guest_" + randToken(6)
	pass := randToken(20)
	if _, err := s.db.Pool.Exec(r.Context(), s.q.CreateGuestSession,
		tenantID, user, pass, flowID, int(guestSessionTTL.Seconds()), clientRef,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "session create failed")
		return
	}

	writeJSON(w, http.StatusOK, guestCredentialsResponse{
		SipUsername: user,
		SipPassword: pass,
		SipDomain:   sipDomain,
		WSURL:       s.wsURL,
		ICEServers:  s.iceServers,
		DialTarget:  guestDialTarget,
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

// originAllowed reports whether a request Origin is permitted. Fail-closed:
// an empty Origin (non-browser caller) or an empty allowlist (unconfigured
// tenant) is rejected. Match is exact against normalized scheme://host[:port].
func originAllowed(origin string, allowed []string) bool {
	if origin == "" || len(allowed) == 0 {
		return false
	}
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
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

// randToken returns a lowercase hex string of n random bytes (2n chars).
func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

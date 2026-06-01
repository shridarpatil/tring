package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
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
		tenantID    uuid.UUID
		sipDomain   string
		supportFlow *uuid.UUID
	)
	err := s.db.Pool.QueryRow(r.Context(), `
		SELECT id, sip_domain, support_flow_id
		FROM tenants WHERE public_key = $1
	`, req.PublicKey).Scan(&tenantID, &sipDomain, &supportFlow)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "unknown public key")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	flowID, err := s.resolveGuestFlow(r.Context(), tenantID, req.FlowID, supportFlow)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Reap expired sessions lazily so the table stays bounded without a
	// background worker. Cheap (indexed on expires_at) and best-effort.
	_, _ = s.db.Pool.Exec(r.Context(), `DELETE FROM guest_sessions WHERE expires_at < now()`)

	user := "guest_" + randToken(6)
	pass := randToken(20)
	_, err = s.db.Pool.Exec(r.Context(), `
		INSERT INTO guest_sessions (tenant_id, sip_username, sip_password, flow_id, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))
	`, tenantID, user, pass, flowID, int(guestSessionTTL.Seconds()))
	if err != nil {
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
	return uuid.Nil, errors.New("no flow specified and tenant has no support flow configured")
}

// assertFlowUsable confirms a flow exists, belongs to the tenant, and is
// active. Returns a caller-safe error otherwise.
func (s *Server) assertFlowUsable(ctx context.Context, flowID, tenantID uuid.UUID) error {
	var ok bool
	err := s.db.Pool.QueryRow(ctx, `
		SELECT true FROM flows WHERE id = $1 AND tenant_id = $2 AND is_active
	`, flowID, tenantID).Scan(&ok)
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
	PublicKey     string     `json:"public_key"`
	SupportFlowID *uuid.UUID `json:"support_flow_id"`
}

// handleGetGuestConfig returns the tenant's public widget key + the flow
// guest web-calls are routed into. The console reads this to render the
// embed snippet and the support-flow picker.
func (s *Server) handleGetGuestConfig(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var resp guestConfigResponse
	err := s.db.Pool.QueryRow(r.Context(), `
		SELECT COALESCE(public_key, ''), support_flow_id FROM tenants WHERE id = $1
	`, t.ID).Scan(&resp.PublicKey, &resp.SupportFlowID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type patchGuestConfigRequest struct {
	// SupportFlowID: a flow UUID to pin guest calls to, or "" to clear it.
	SupportFlowID *string `json:"support_flow_id"`
}

// handlePatchGuestConfig sets which flow guest web-calls land in.
func (s *Server) handlePatchGuestConfig(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req patchGuestConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.SupportFlowID == nil {
		writeError(w, http.StatusBadRequest, "support_flow_id required")
		return
	}

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

	if _, err := s.db.Pool.Exec(r.Context(),
		`UPDATE tenants SET support_flow_id = $1 WHERE id = $2`, flowID, t.ID,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	s.handleGetGuestConfig(w, r)
}

// randToken returns a lowercase hex string of n random bytes (2n chars).
func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/freeswitch"
)

// Trunk is the API/DB shape for a tenant-scoped SIP trunk. Authored from
// the admin console, persisted to the `trunks` table, rendered to
// FreeSWITCH gateway XML by internal/freeswitch.WriteGateway and picked
// up via a sofia profile rescan on save.
type Trunk struct {
	ID           uuid.UUID       `json:"id"`
	TenantID     uuid.UUID       `json:"tenant_id"`
	Name         string          `json:"name"`
	ProviderType string          `json:"provider_type"`
	SIPUsername  string          `json:"sip_username"`
	SIPPassword  string          `json:"sip_password,omitempty"` // omitted on list responses
	SIPRealm     string          `json:"sip_realm"`
	Register     bool            `json:"register"`
	ProxyIP      string          `json:"proxy_ip"`
	AuthACLCIDR  string          `json:"auth_acl_cidr"`
	Active       bool            `json:"active"`
	ExtraParams  json.RawMessage `json:"extra_params"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	// Live state from sofia status — populated on responses, not in DB.
	// Values: online | offline | connecting | not_loaded | unknown.
	Status   string   `json:"status,omitempty"`
	StateRaw string   `json:"state_raw,omitempty"` // raw FS state for debugging
	RegState string   `json:"reg_state,omitempty"` // sofia registration state: REGED, NOREG, FAIL_WAIT, …
	PingMS   *float64 `json:"ping_ms,omitempty"`   // last OPTIONS round trip; nil until a ping is answered
	// Calls through this trunk since FreeSWITCH (re)loaded it — what shows
	// whether calls actually work, not just keepalives. Nil when not loaded.
	Calls *TrunkCalls `json:"calls,omitempty"`
}

// TrunkCalls are a trunk gateway's call counters, as FreeSWITCH counts them
// since Since. Inbound counts are only reported for registered trunks: for
// IP-auth trunks FreeSWITCH doesn't attribute inbound calls to the gateway,
// so a zero there would wrongly read as "no calls arrived".
type TrunkCalls struct {
	Out       int       `json:"out"`
	FailedOut int       `json:"failed_out"`
	In        *int      `json:"in,omitempty"`
	FailedIn  *int      `json:"failed_in,omitempty"`
	Since     time.Time `json:"since"`
}

// trunkCalls builds a trunk's call counters from its gateway health; nil when
// FreeSWITCH has no such gateway.
func trunkCalls(h freeswitch.GatewayHealth, register bool, now time.Time) *TrunkCalls {
	if h.State == "" {
		return nil
	}
	c := &TrunkCalls{
		Out:       h.CallsOut,
		FailedOut: h.FailedOut,
		Since:     now.Add(-time.Duration(h.UptimeSec * float64(time.Second))).Truncate(time.Second),
	}
	if register {
		in, failed := h.CallsIn, h.FailedIn
		c.In, c.FailedIn = &in, &failed
	}
	return c
}

type createTrunkRequest struct {
	Name         string          `json:"name"`
	ProviderType string          `json:"provider_type,omitempty"`
	SIPUsername  string          `json:"sip_username,omitempty"`
	SIPPassword  string          `json:"sip_password,omitempty"`
	SIPRealm     string          `json:"sip_realm,omitempty"`
	Register     *bool           `json:"register,omitempty"`
	ProxyIP      string          `json:"proxy_ip,omitempty"`
	AuthACLCIDR  string          `json:"auth_acl_cidr,omitempty"`
	Active       *bool           `json:"active,omitempty"`
	ExtraParams  json.RawMessage `json:"extra_params,omitempty"`
}

type patchTrunkRequest struct {
	Name         *string         `json:"name,omitempty"`
	ProviderType *string         `json:"provider_type,omitempty"`
	SIPUsername  *string         `json:"sip_username,omitempty"`
	SIPPassword  *string         `json:"sip_password,omitempty"`
	SIPRealm     *string         `json:"sip_realm,omitempty"`
	Register     *bool           `json:"register,omitempty"`
	ProxyIP      *string         `json:"proxy_ip,omitempty"`
	AuthACLCIDR  *string         `json:"auth_acl_cidr,omitempty"`
	Active       *bool           `json:"active,omitempty"`
	ExtraParams  json.RawMessage `json:"extra_params,omitempty"`
}

func scanTrunk(scanner interface{ Scan(...interface{}) error }) (Trunk, error) {
	var t Trunk
	err := scanner.Scan(
		&t.ID, &t.TenantID, &t.Name, &t.ProviderType,
		&t.SIPUsername, &t.SIPPassword, &t.SIPRealm,
		&t.Register, &t.ProxyIP, &t.AuthACLCIDR,
		&t.Active, &t.ExtraParams, &t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}

// trunkToGateway converts a DB Trunk into the freeswitch.Gateway shape
// the XML writer consumes. Kept in this package so the API layer owns
// the field mapping; internal/freeswitch only knows about the rendered
// gateway, not Tring's DB schema.
//
// extra_params may carry {"from_domain"} for carriers that check the From
// host and {"auth_username"} for register-mode carriers whose digest user
// differs from the registered identity (IMS); a missing or malformed value
// just omits them.
func trunkToGateway(t Trunk) freeswitch.Gateway {
	var extra struct {
		FromDomain   string `json:"from_domain"`
		AuthUsername string `json:"auth_username"`
	}
	if json.Unmarshal(t.ExtraParams, &extra) != nil {
		extra.FromDomain, extra.AuthUsername = "", ""
	}
	return freeswitch.Gateway{
		ID:           t.ID.String(),
		Name:         freeswitch.GatewayName(t.ID),
		Active:       t.Active,
		Register:     t.Register,
		Username:     t.SIPUsername,
		AuthUsername: extra.AuthUsername,
		Password:     t.SIPPassword,
		Realm:        t.SIPRealm,
		ProxyIP:      t.ProxyIP,
		AuthACLCIDR:  t.AuthACLCIDR,
		FromDomain:   extra.FromDomain,
	}
}

// rerenderAllGateways writes the full set of dynamic gateway XMLs, then
// triggers a sofia profile rescan so FreeSWITCH state matches the DB. Called
// after any trunk create/update/delete. Returns an error if the XML can't be
// written OR the rescan fails — callers MUST surface it. The DB row has
// already been committed by then, so a failure here means FreeSWITCH is out of
// sync with the DB until POST /v1/trunks/reload (or the next successful trunk
// op) re-renders and rescans.
func (s *Server) rerenderAllGateways(ctx context.Context) error {
	rows, err := s.db.Pool.Query(ctx, s.q.ListActiveTrunks)
	if err != nil {
		return err
	}
	defer rows.Close()
	var gws []freeswitch.Gateway
	for rows.Next() {
		t, err := scanTrunk(rows)
		if err != nil {
			return err
		}
		gws = append(gws, trunkToGateway(t))
	}
	stale, err := freeswitch.WriteGateways(gws)
	if err != nil {
		return fmt.Errorf("write gateway xml: %w", err)
	}
	if err := s.fs.RescanExternalProfile(ctx, stale); err != nil {
		return fmt.Errorf("freeswitch rescan: %w", err)
	}
	return nil
}

// syncTrunkError writes a 502 telling the caller their DB change committed but
// FreeSWITCH didn't pick it up — so they don't read a 2xx as "live" and they
// know POST /v1/trunks/reload is the retry. `did` is the past-tense verb, e.g.
// "saved" / "updated" / "deleted".
// The underlying error (file paths, ESL replies) is logged, not returned.
func (s *Server) syncTrunkError(w http.ResponseWriter, r *http.Request, did string, err error) {
	s.writeServerError(w, r, http.StatusBadGateway,
		"trunk "+did+" but FreeSWITCH did not reload — retry with POST /v1/trunks/reload", err)
}

func (s *Server) handleCreateTrunk(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createTrunkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.ProviderType == "" {
		req.ProviderType = "generic"
	}
	var extra interface{}
	if len(req.ExtraParams) > 0 {
		extra = req.ExtraParams
	}
	row := s.db.Pool.QueryRow(r.Context(), s.q.CreateTrunk,
		t.ID, req.Name, req.ProviderType, req.SIPUsername, req.SIPPassword, req.SIPRealm,
		req.Register, req.ProxyIP, req.AuthACLCIDR, req.Active, extra,
	)
	tr, err := scanTrunk(row)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "a trunk with this name already exists")
		return
	}
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "trunk insert failed", err)
		return
	}
	if err := s.rerenderAllGateways(r.Context()); err != nil {
		s.syncTrunkError(w, r, "saved", err)
		return
	}
	writeJSON(w, http.StatusCreated, tr)
}

func (s *Server) handleListTrunks(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListTrunks, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []Trunk{}
	for rows.Next() {
		tr, err := scanTrunk(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		// Don't leak password in list responses.
		tr.SIPPassword = ""
		out = append(out, tr)
	}
	// Best-effort: enrich with live FS gateway health. If ESL is down
	// (dev offline), no gateway is found and trunks list as not_loaded.
	// For IP-auth trunks the reg-state is meaningless, so freeswitch.Status
	// reads sofia's OPTIONS-ping verdict instead — see gateway_status.go.
	healths, _ := s.fs.GatewayHealths(r.Context())
	now := time.Now()
	for i := range out {
		h := healths[freeswitch.GatewayName(out[i].ID)]
		out[i].Status = freeswitch.Status(h, out[i].Register)
		out[i].StateRaw = h.Detail()
		out[i].RegState = h.State
		if h.PingUp && h.PingRTT > 0 {
			ms := h.PingRTT
			out[i].PingMS = &ms
		}
		out[i].Calls = trunkCalls(h, out[i].Register, now)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetTrunk(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row := s.db.Pool.QueryRow(r.Context(), s.q.GetTrunk, id, t.ID)
	tr, err := scanTrunk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "trunk not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, tr)
}

func (s *Server) handlePatchTrunk(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req patchTrunkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var extra interface{}
	if len(req.ExtraParams) > 0 {
		extra = req.ExtraParams
	}
	row := s.db.Pool.QueryRow(r.Context(), s.q.UpdateTrunk,
		req.Name, req.ProviderType, req.SIPUsername, req.SIPPassword, req.SIPRealm,
		req.Register, req.ProxyIP, req.AuthACLCIDR, req.Active, extra,
		id, t.ID,
	)
	tr, err := scanTrunk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "trunk not found")
		return
	}
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "a trunk with this name already exists")
		return
	}
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "update failed", err)
		return
	}
	if err := s.rerenderAllGateways(r.Context()); err != nil {
		s.syncTrunkError(w, r, "updated", err)
		return
	}
	writeJSON(w, http.StatusOK, tr)
}

func (s *Server) handleDeleteTrunk(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.DeleteTrunk, id, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "trunk not found")
		return
	}
	if err := s.rerenderAllGateways(r.Context()); err != nil {
		s.syncTrunkError(w, r, "deleted", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleReloadTrunks re-renders all gateway XMLs and rescans the sofia
// external profile. Useful as a manual "kick" if FS state has drifted
// from the DB (e.g. after a crash before a write completed).
func (s *Server) handleReloadTrunks(w http.ResponseWriter, r *http.Request) {
	if err := s.rerenderAllGateways(r.Context()); err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "reload failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

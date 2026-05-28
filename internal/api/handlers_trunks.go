package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whatomate/tring/internal/freeswitch"
)

// Trunk is the API/DB shape for a tenant-scoped SIP trunk. Authored from
// the admin console, persisted to the `trunks` table, rendered to
// FreeSWITCH gateway XML by internal/freeswitch.WriteGateway and picked
// up via a sofia profile rescan on save.
type Trunk struct {
	ID            uuid.UUID       `json:"id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	Name          string          `json:"name"`
	ProviderType  string          `json:"provider_type"`
	SIPUsername   string          `json:"sip_username"`
	SIPPassword   string          `json:"sip_password,omitempty"` // omitted on list responses
	SIPRealm      string          `json:"sip_realm"`
	Register      bool            `json:"register"`
	ProxyIP       string          `json:"proxy_ip"`
	AuthACLCIDR   string          `json:"auth_acl_cidr"`
	Active        bool            `json:"active"`
	ExtraParams   json.RawMessage `json:"extra_params"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	// Live state from sofia status — populated on responses, not in DB.
	// Values: online | offline | connecting | not_loaded | unknown.
	Status     string `json:"status,omitempty"`
	StateRaw   string `json:"state_raw,omitempty"`   // raw FS state for debugging
}

type createTrunkRequest struct {
	Name          string          `json:"name"`
	ProviderType  string          `json:"provider_type,omitempty"`
	SIPUsername   string          `json:"sip_username,omitempty"`
	SIPPassword   string          `json:"sip_password,omitempty"`
	SIPRealm      string          `json:"sip_realm,omitempty"`
	Register      *bool           `json:"register,omitempty"`
	ProxyIP       string          `json:"proxy_ip,omitempty"`
	AuthACLCIDR   string          `json:"auth_acl_cidr,omitempty"`
	Active        *bool           `json:"active,omitempty"`
	ExtraParams   json.RawMessage `json:"extra_params,omitempty"`
}

type patchTrunkRequest struct {
	Name          *string         `json:"name,omitempty"`
	ProviderType  *string         `json:"provider_type,omitempty"`
	SIPUsername   *string         `json:"sip_username,omitempty"`
	SIPPassword   *string         `json:"sip_password,omitempty"`
	SIPRealm      *string         `json:"sip_realm,omitempty"`
	Register      *bool           `json:"register,omitempty"`
	ProxyIP       *string         `json:"proxy_ip,omitempty"`
	AuthACLCIDR   *string         `json:"auth_acl_cidr,omitempty"`
	Active        *bool           `json:"active,omitempty"`
	ExtraParams   json.RawMessage `json:"extra_params,omitempty"`
}

const trunkSelectColumns = `
	id, tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
	register, proxy_ip, auth_acl_cidr, active, extra_params, created_at, updated_at
`

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
func trunkToGateway(t Trunk) freeswitch.Gateway {
	return freeswitch.Gateway{
		ID:          t.ID.String(),
		Name:        freeswitch.GatewayName(t.ID),
		Active:      t.Active,
		Register:    t.Register,
		Username:    t.SIPUsername,
		Password:    t.SIPPassword,
		Realm:       t.SIPRealm,
		ProxyIP:     t.ProxyIP,
		AuthACLCIDR: t.AuthACLCIDR,
	}
}

// rerenderAllGateways writes the full set of dynamic gateway XMLs for
// the tenant, then triggers a sofia profile rescan. Called after any
// trunk create/update/delete so FreeSWITCH state matches the DB.
func (s *Server) rerenderAllGateways(ctx context.Context) error {
	rows, err := s.db.Pool.Query(ctx,
		`SELECT `+trunkSelectColumns+` FROM trunks WHERE active`)
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
	if err := freeswitch.WriteGateways(gws); err != nil {
		return err
	}
	// Best-effort rescan: log but don't fail the API call if FS is down.
	if err := s.fs.RescanExternalProfile(ctx); err != nil {
		// Logged inside the client; the API still succeeded.
		_ = err
	}
	return nil
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
	row := s.db.Pool.QueryRow(r.Context(), `
		INSERT INTO trunks (
			tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
			register, proxy_ip, auth_acl_cidr, active, extra_params
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			COALESCE($7, false), $8, $9, COALESCE($10, true),
			COALESCE($11::jsonb, '{}'::jsonb)
		)
		RETURNING `+trunkSelectColumns,
		t.ID, req.Name, req.ProviderType, req.SIPUsername, req.SIPPassword, req.SIPRealm,
		req.Register, req.ProxyIP, req.AuthACLCIDR, req.Active, extra,
	)
	tr, err := scanTrunk(row)
	if err != nil {
		writeError(w, http.StatusConflict, "trunk insert failed: "+err.Error())
		return
	}
	_ = s.rerenderAllGateways(r.Context())
	writeJSON(w, http.StatusCreated, tr)
}

func (s *Server) handleListTrunks(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(),
		`SELECT `+trunkSelectColumns+` FROM trunks WHERE tenant_id = $1 ORDER BY name`,
		t.ID)
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
	// Best-effort: enrich with live FS gateway state. If ESL is down
	// (dev offline), trunks still list with status="unknown".
	states, _ := s.fs.GatewayStatuses(r.Context())
	for i := range out {
		gw := freeswitch.GatewayName(out[i].ID)
		raw := states[gw]
		out[i].StateRaw = raw
		out[i].Status = freeswitch.StatusFromState(raw, out[i].Register)

		// For IP-auth trunks, sofia state is uninformative about peer
		// reachability (NOREG even when the SBC is down). Probe with a
		// SIP OPTIONS to find out for real. ~1s budget per probe; only
		// runs when the trunk has a configured proxy and is "online"
		// per state — flips to offline if the probe times out.
		if !out[i].Register && out[i].ProxyIP != "" && out[i].Status == "online" {
			ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
			ok := freeswitch.OptionsPing(ctx, out[i].ProxyIP, 1*time.Second)
			cancel()
			if !ok {
				out[i].Status = "offline"
				out[i].StateRaw = raw + " (probe failed)"
			}
		}
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
	row := s.db.Pool.QueryRow(r.Context(),
		`SELECT `+trunkSelectColumns+` FROM trunks WHERE id = $1 AND tenant_id = $2`,
		id, t.ID)
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
	row := s.db.Pool.QueryRow(r.Context(), `
		UPDATE trunks SET
			name          = COALESCE($1, name),
			provider_type = COALESCE($2, provider_type),
			sip_username  = COALESCE($3, sip_username),
			sip_password  = COALESCE($4, sip_password),
			sip_realm     = COALESCE($5, sip_realm),
			register      = COALESCE($6, register),
			proxy_ip      = COALESCE($7, proxy_ip),
			auth_acl_cidr = COALESCE($8, auth_acl_cidr),
			active        = COALESCE($9, active),
			extra_params  = COALESCE($10::jsonb, extra_params),
			updated_at    = now()
		WHERE id = $11 AND tenant_id = $12
		RETURNING `+trunkSelectColumns,
		req.Name, req.ProviderType, req.SIPUsername, req.SIPPassword, req.SIPRealm,
		req.Register, req.ProxyIP, req.AuthACLCIDR, req.Active, extra,
		id, t.ID,
	)
	tr, err := scanTrunk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "trunk not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	_ = s.rerenderAllGateways(r.Context())
	writeJSON(w, http.StatusOK, tr)
}

func (s *Server) handleDeleteTrunk(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(),
		`DELETE FROM trunks WHERE id = $1 AND tenant_id = $2`,
		id, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "trunk not found")
		return
	}
	_ = s.rerenderAllGateways(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// handleReloadTrunks re-renders all gateway XMLs and rescans the sofia
// external profile. Useful as a manual "kick" if FS state has drifted
// from the DB (e.g. after a crash before a write completed).
func (s *Server) handleReloadTrunks(w http.ResponseWriter, r *http.Request) {
	if err := s.rerenderAllGateways(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "reload failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

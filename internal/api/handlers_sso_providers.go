package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Admin CRUD for OIDC SSO providers (/v1/sso-providers). A provider claims
// email domains globally (sso_provider_domains.domain is the primary key), so
// the login page can route an email to its IdP without knowing the user. The
// client secret is sealed at rest and never returned.

// ssoProvider is the API shape; it deliberately has no secret field.
type ssoProvider struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	IssuerURL  string    `json:"issuer_url"`
	ClientID   string    `json:"client_id"`
	Domains    []string  `json:"domains"`
	RequireSSO bool      `json:"require_sso"`
	Enabled    bool      `json:"enabled"`
	// RequireHostedDomain (Google only): the ID token must carry an hd claim
	// naming one of Domains, i.e. a Workspace-managed account.
	RequireHostedDomain bool      `json:"require_hosted_domain"`
	SSOOnlyUsers        int       `json:"sso_only_users"`
	CreatedAt           time.Time `json:"created_at"`
}

// normalizeDomain lower-cases and validates an email domain claim.
func normalizeDomain(d string) (string, bool) {
	d = strings.ToLower(strings.TrimSpace(d))
	if !strings.Contains(d, ".") || strings.ContainsAny(d, "@/ \t") ||
		strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
		return "", false
	}
	return d, true
}

// normalizeDomains validates, lower-cases and de-duplicates a domain list.
func normalizeDomains(in []string) ([]string, bool) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, d := range in {
		n, ok := normalizeDomain(d)
		if !ok {
			return nil, false
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, true
}

// discoverIssuer fetches the issuer's OIDC discovery document through the
// SSRF-guarded client. Used on save (reject unreachable issuers early) and by
// the login flow.
func (s *Server) discoverIssuer(ctx context.Context, issuer string) (*oidc.Provider, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return oidc.NewProvider(oidc.ClientContext(ctx, s.ssoHTTP), issuer)
}

func validIssuerURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.IsAbs() && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http")
}

func (s *Server) handleSSOCallbackURL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"callback_url": s.consolePublicURL + "/v1/console/sso/callback"})
}

func (s *Server) handleListSSOProviders(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListSSOProviders, p.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list sso providers failed")
		return
	}
	defer rows.Close()
	out := []ssoProvider{}
	for rows.Next() {
		var sp ssoProvider
		if err := rows.Scan(&sp.ID, &sp.Name, &sp.IssuerURL, &sp.ClientID, &sp.RequireSSO, &sp.Enabled,
			&sp.CreatedAt, &sp.Domains, &sp.RequireHostedDomain, &sp.SSOOnlyUsers); err != nil {
			writeError(w, http.StatusInternalServerError, "list sso providers failed")
			return
		}
		out = append(out, sp)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "list sso providers failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getSSOProvider loads one provider (without the secret) for a response body.
func (s *Server) getSSOProvider(ctx context.Context, tenantID, id uuid.UUID) (ssoProvider, string, error) {
	var sp ssoProvider
	var secret string
	err := s.db.Pool.QueryRow(ctx, s.q.GetSSOProvider, tenantID, id).Scan(&sp.ID, &sp.Name, &sp.IssuerURL,
		&sp.ClientID, &secret, &sp.RequireSSO, &sp.Enabled, &sp.CreatedAt, &sp.Domains, &sp.RequireHostedDomain)
	return sp, secret, err
}

type createSSOProviderRequest struct {
	Name                string   `json:"name"`
	IssuerURL           string   `json:"issuer_url"`
	ClientID            string   `json:"client_id"`
	ClientSecret        string   `json:"client_secret"`
	Domains             []string `json:"domains"`
	RequireSSO          bool     `json:"require_sso"`
	Enabled             *bool    `json:"enabled"`
	RequireHostedDomain bool     `json:"require_hosted_domain"`
}

func (s *Server) handleCreateSSOProvider(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	var req createSSOProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.IssuerURL = strings.TrimSpace(req.IssuerURL)
	req.ClientID = strings.TrimSpace(req.ClientID)
	if req.Name == "" || req.ClientID == "" || req.ClientSecret == "" || !validIssuerURL(req.IssuerURL) {
		writeError(w, http.StatusBadRequest, "name, issuer_url, client_id and client_secret are required")
		return
	}
	domains, ok := normalizeDomains(req.Domains)
	if !ok || len(domains) == 0 {
		writeError(w, http.StatusBadRequest, "domains must be a non-empty list of valid email domains")
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	if _, err := s.discoverIssuer(r.Context(), req.IssuerURL); err != nil {
		writeError(w, http.StatusBadRequest, "issuer discovery failed")
		return
	}

	ctx := r.Context()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create sso provider failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id uuid.UUID
	if err := tx.QueryRow(ctx, s.q.CreateSSOProvider, p.TenantID, req.Name, req.IssuerURL, req.ClientID,
		s.sealSecret(req.ClientSecret), req.RequireSSO, enabled, req.RequireHostedDomain).Scan(&id); err != nil {
		writeError(w, http.StatusInternalServerError, "create sso provider failed")
		return
	}
	if code, msg := s.insertSSODomains(ctx, tx, p.TenantID, id, domains); code != 0 {
		writeError(w, code, msg)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "create sso provider failed")
		return
	}
	sp, _, err := s.getSSOProvider(ctx, p.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create sso provider failed")
		return
	}
	writeJSON(w, http.StatusCreated, sp)
}

// insertSSODomains returns 0 or the status + message to answer with.
func (s *Server) insertSSODomains(ctx context.Context, tx pgx.Tx, tenantID, providerID uuid.UUID, domains []string) (int, string) {
	for _, d := range domains {
		if _, err := tx.Exec(ctx, s.q.InsertSSOProviderDomain, d, providerID, tenantID); err != nil {
			if isUniqueViolation(err) {
				return http.StatusConflict, "domain already used by another SSO provider"
			}
			return http.StatusInternalServerError, "save sso domains failed"
		}
	}
	return 0, ""
}

type patchSSOProviderRequest struct {
	Name                *string   `json:"name"`
	IssuerURL           *string   `json:"issuer_url"`
	ClientID            *string   `json:"client_id"`
	ClientSecret        *string   `json:"client_secret"`
	Domains             *[]string `json:"domains"`
	RequireSSO          *bool     `json:"require_sso"`
	Enabled             *bool     `json:"enabled"`
	RequireHostedDomain *bool     `json:"require_hosted_domain"`
}

func (s *Server) handlePatchSSOProvider(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req patchSSOProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ctx := r.Context()
	cur, _, err := s.getSSOProvider(ctx, p.TenantID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "sso provider not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update sso provider failed")
		return
	}

	trim := func(v *string) *string {
		if v == nil {
			return nil
		}
		t := strings.TrimSpace(*v)
		return &t
	}
	req.Name, req.IssuerURL, req.ClientID = trim(req.Name), trim(req.IssuerURL), trim(req.ClientID)
	if (req.Name != nil && *req.Name == "") || (req.ClientID != nil && *req.ClientID == "") {
		writeError(w, http.StatusBadRequest, "name and client_id cannot be empty")
		return
	}
	if req.IssuerURL != nil {
		if !validIssuerURL(*req.IssuerURL) {
			writeError(w, http.StatusBadRequest, "invalid issuer_url")
			return
		}
		if *req.IssuerURL != cur.IssuerURL {
			if _, err := s.discoverIssuer(ctx, *req.IssuerURL); err != nil {
				writeError(w, http.StatusBadRequest, "issuer discovery failed")
				return
			}
		}
	}
	// "" means keep, same as absent.
	if req.ClientSecret != nil && *req.ClientSecret == "" {
		req.ClientSecret = nil
	}
	var domains []string
	if req.Domains != nil {
		var ok bool
		if domains, ok = normalizeDomains(*req.Domains); !ok || len(domains) == 0 {
			writeError(w, http.StatusBadRequest, "domains must be a non-empty list of valid email domains")
			return
		}
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update sso provider failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, s.q.UpdateSSOProvider, p.TenantID, id, req.Name, req.IssuerURL, req.ClientID,
		s.sealSecretPtr(req.ClientSecret), req.RequireSSO, req.Enabled, req.RequireHostedDomain); err != nil {
		writeError(w, http.StatusInternalServerError, "update sso provider failed")
		return
	}
	if req.Domains != nil {
		if _, err := tx.Exec(ctx, s.q.DeleteSSOProviderDomains, p.TenantID, id); err != nil {
			writeError(w, http.StatusInternalServerError, "update sso provider failed")
			return
		}
		if code, msg := s.insertSSODomains(ctx, tx, p.TenantID, id, domains); code != 0 {
			writeError(w, code, msg)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "update sso provider failed")
		return
	}
	sp, _, err := s.getSSOProvider(ctx, p.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update sso provider failed")
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) handleDeleteSSOProvider(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.DeleteSSOProvider, p.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete sso provider failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "sso provider not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

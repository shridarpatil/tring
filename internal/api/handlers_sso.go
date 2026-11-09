package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

// Console SSO login (OIDC authorization code + PKCE). Flow:
//
//	POST /v1/console/login/start   email → password, or the IdP's auth URL
//	GET  /v1/console/sso/callback  IdP → verify → one-time code → /login?sso_code=
//	POST /v1/console/sso/exchange  code → the same response as password login
//
// The state record and the one-time code live in Redis and are consumed with
// GETDEL, so each is single-use; without Redis SSO is unavailable. The console
// token never appears in a URL: only the 60 s one-time code does.

const (
	ssoStateTTL = 10 * time.Minute
	ssoCodeTTL  = 60 * time.Second

	ssoStatePrefix = "sso:state:"
	ssoCodePrefix  = "sso:code:"
)

// ssoRedirectRe is safeRedirect in web/src/lib/roles.ts: only the console's
// own entry points, so the post-login hop can't become an open redirect.
var ssoRedirectRe = regexp.MustCompile(`^/(admin|agent)(/|\?|$)`)

func safeSSORedirect(r string) string {
	if ssoRedirectRe.MatchString(r) {
		return r
	}
	return ""
}

// ssoState is what start hands the callback through Redis.
type ssoState struct {
	ProviderID uuid.UUID `json:"provider_id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	Email      string    `json:"email"`
	Nonce      string    `json:"nonce"`
	Verifier   string    `json:"verifier"`
	Redirect   string    `json:"redirect"`
}

// ssoRandom is 32 random bytes, base64url: state, nonce and one-time codes.
func ssoRandom() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// emailDomain is the normalized domain after the LAST '@' ("" if none), so a
// quoted local part containing '@' can't smuggle in another domain.
func emailDomain(email string) string {
	i := strings.LastIndex(email, "@")
	if i < 0 {
		return ""
	}
	d, ok := normalizeDomain(email[i+1:])
	if !ok {
		return ""
	}
	return d
}

func (s *Server) ssoOAuthConfig(p *oidc.Provider, clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  s.consolePublicURL + "/v1/console/sso/callback",
		Scopes:       []string{oidc.ScopeOpenID, "email"},
	}
}

type ssoStartRequest struct {
	Email    string `json:"email"`
	Redirect string `json:"redirect"`
}

var passwordMethod = map[string]string{"method": "password"}

// handleSSOStart routes an email to its SSO provider or to the password form.
// The answer depends only on the email's domain, never on whether a user
// exists, so it can't be used to enumerate accounts.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	if retry, ok := s.ssoStartByIP.take(clientIP(r)); !ok {
		writeTooManyRequests(w, retry)
		return
	}
	var req ssoStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	domain := emailDomain(email)
	if domain == "" {
		writeError(w, http.StatusBadRequest, "email required")
		return
	}

	ctx := r.Context()
	var (
		st                       ssoState
		issuer, clientID, secret string
		requireSSO               bool
	)
	err := s.db.Pool.QueryRow(ctx, s.q.GetSSODomain, domain).
		Scan(&st.ProviderID, &st.TenantID, &issuer, &clientID, &secret, &requireSSO)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, passwordMethod)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if s.cache == nil {
		// Password login still enforces require_sso, so enforced domains
		// fail closed rather than open.
		s.log.Warn("sso unavailable: redis not configured", "tenant_id", st.TenantID, "provider_id", st.ProviderID)
		writeJSON(w, http.StatusOK, passwordMethod)
		return
	}
	provider, err := s.discoverIssuer(ctx, issuer)
	if err != nil {
		s.log.Warn("sso unavailable: issuer discovery failed", "tenant_id", st.TenantID,
			"provider_id", st.ProviderID, "err", err)
		writeJSON(w, http.StatusOK, passwordMethod)
		return
	}

	state, err1 := ssoRandom()
	nonce, err2 := ssoRandom()
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusInternalServerError, "sso start failed")
		return
	}
	st.Email = email
	st.Nonce = nonce
	st.Verifier = oauth2.GenerateVerifier()
	st.Redirect = safeSSORedirect(req.Redirect)
	rec, err := json.Marshal(st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sso start failed")
		return
	}
	// A fresh 256-bit key can't already exist, so !ok means Redis misbehaved.
	if ok, err := s.cache.SetNX(ctx, ssoStatePrefix+state, rec, ssoStateTTL); err != nil || !ok {
		s.log.Warn("sso unavailable: store state failed", "tenant_id", st.TenantID,
			"provider_id", st.ProviderID, "err", err)
		writeJSON(w, http.StatusOK, passwordMethod)
		return
	}

	authURL := s.ssoOAuthConfig(provider, clientID, "").AuthCodeURL(state,
		oauth2.S256ChallengeOption(st.Verifier),
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("login_hint", email),
	)
	writeJSON(w, http.StatusOK, map[string]string{"method": "sso", "redirect_url": authURL})
}

// ssoFailure is a callback rejection: code is the generic reason the login
// page shows, reason goes to the log only.
type ssoFailure struct {
	code   string // expired | no_account | failed
	reason string
	err    error
}

func ssoFail(code, reason string, err error) *ssoFailure {
	return &ssoFailure{code: code, reason: reason, err: err}
}

// handleSSOCallback is where the IdP sends the browser back. Every outcome is
// a 302 to /login; the console session is parked under a one-time code.
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	// Consume the state first, whatever else the IdP sent (including
	// ?error=...), so it can never be replayed.
	var st ssoState
	if state := q.Get("state"); state != "" {
		raw, ok, err := s.cache.GetDel(ctx, ssoStatePrefix+state)
		if err != nil {
			s.ssoRedirectFail(w, r, st, ssoFail("expired", "read state", err))
			return
		}
		if !ok {
			s.ssoRedirectFail(w, r, st, ssoFail("expired", "unknown or used state", nil))
			return
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			s.ssoRedirectFail(w, r, st, ssoFail("failed", "decode state", err))
			return
		}
	} else {
		s.ssoRedirectFail(w, r, st, ssoFail("expired", "missing state", nil))
		return
	}

	userID, email, fail := s.ssoVerifyCallback(ctx, st, q)
	if fail != nil {
		s.ssoRedirectFail(w, r, st, fail)
		return
	}
	resp, err := s.consoleSession(ctx, userID, st.TenantID, email)
	if err != nil {
		s.ssoRedirectFail(w, r, st, ssoFail("failed", "build session", err))
		return
	}
	body, err := json.Marshal(resp)
	if err != nil {
		s.ssoRedirectFail(w, r, st, ssoFail("failed", "encode session", err))
		return
	}
	code, err := ssoRandom()
	if err != nil {
		s.ssoRedirectFail(w, r, st, ssoFail("failed", "random code", err))
		return
	}
	if ok, err := s.cache.SetNX(ctx, ssoCodePrefix+code, body, ssoCodeTTL); err != nil || !ok {
		s.ssoRedirectFail(w, r, st, ssoFail("failed", "store code", err))
		return
	}

	s.log.Info("sso login", "tenant_id", st.TenantID, "provider_id", st.ProviderID, "user_id", userID)
	target := "/login?sso_code=" + url.QueryEscape(code)
	if st.Redirect != "" {
		target += "&redirect=" + url.QueryEscape(st.Redirect)
	}
	ssoNoStore(w)
	http.Redirect(w, r, target, http.StatusFound)
}

// ssoVerifyCallback exchanges the code and checks the ID token and the user.
// It returns the user to sign in, or why not.
func (s *Server) ssoVerifyCallback(ctx context.Context, st ssoState, q url.Values) (uuid.UUID, string, *ssoFailure) {
	code := q.Get("code")
	if code == "" {
		// The IdP's own error code is safe and useful to log; its free-text
		// description is not ours to trust.
		return uuid.Nil, "", ssoFail("failed", "no code from idp: "+q.Get("error"), nil)
	}

	var (
		pid                            uuid.UUID
		name, issuer, clientID, sealed string
		requireSSO, enabled            bool
		createdAt                      time.Time
		domains                        []string
	)
	err := s.db.Pool.QueryRow(ctx, s.q.GetSSOProvider, st.TenantID, st.ProviderID).
		Scan(&pid, &name, &issuer, &clientID, &sealed, &requireSSO, &enabled, &createdAt, &domains)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ssoFail("failed", "provider gone", nil)
	}
	if err != nil {
		return uuid.Nil, "", ssoFail("failed", "load provider", err)
	}
	if !enabled {
		return uuid.Nil, "", ssoFail("failed", "provider disabled", nil)
	}
	secret, err := s.openSecret(sealed)
	if err != nil {
		return uuid.Nil, "", ssoFail("failed", "open client secret", err)
	}
	provider, err := s.discoverIssuer(ctx, issuer)
	if err != nil {
		return uuid.Nil, "", ssoFail("failed", "issuer discovery", err)
	}

	// Token exchange and JWKS fetches go through the SSRF-guarded client.
	octx := oidc.ClientContext(ctx, s.ssoHTTP)
	tok, err := s.ssoOAuthConfig(provider, clientID, secret).Exchange(octx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return uuid.Nil, "", ssoFail("failed", "code exchange", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return uuid.Nil, "", ssoFail("failed", "no id_token", nil)
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: clientID}).Verify(octx, rawID)
	if err != nil {
		return uuid.Nil, "", ssoFail("failed", "verify id_token", err)
	}
	if st.Nonce == "" || idToken.Nonce != st.Nonce {
		return uuid.Nil, "", ssoFail("failed", "nonce mismatch", nil)
	}
	var claims struct {
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return uuid.Nil, "", ssoFail("failed", "decode claims", err)
	}
	// Bool true, or the string "true" some IdPs send; nothing else.
	if v := string(claims.EmailVerified); v != "true" && v != `"true"` {
		return uuid.Nil, "", ssoFail("failed", "email not verified", nil)
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if d := emailDomain(email); d == "" || !slices.Contains(domains, d) {
		// Exact match only: a provider vouches for its own domains, never a
		// sub- or look-alike domain.
		return uuid.Nil, "", ssoFail("failed", "email domain not owned by provider", nil)
	}

	var (
		userID, userTenant uuid.UUID
		hash               *string
	)
	err = s.db.Pool.QueryRow(ctx, s.q.GetUserByEmail, email).Scan(&userID, &userTenant, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ssoFail("no_account", "no user with this email", nil)
	}
	if err != nil {
		return uuid.Nil, "", ssoFail("failed", "load user", err)
	}
	if userTenant != st.TenantID {
		return uuid.Nil, "", ssoFail("no_account", "user in another tenant", nil)
	}
	return userID, email, nil
}

// ssoRedirectFail logs why (never tokens, codes or secrets) and sends the
// browser to the login page with only the generic reason.
func (s *Server) ssoRedirectFail(w http.ResponseWriter, r *http.Request, st ssoState, f *ssoFailure) {
	args := []any{"tenant_id", st.TenantID, "provider_id", st.ProviderID, "reason", f.reason, "sso_error", f.code}
	if f.err != nil {
		args = append(args, "err", f.err)
	}
	s.log.Warn("sso login failed", args...)
	ssoNoStore(w)
	http.Redirect(w, r, "/login?sso_error="+f.code, http.StatusFound)
}

func ssoNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

type ssoExchangeRequest struct {
	Code string `json:"code"`
}

// handleSSOExchange trades the one-time code for the login response the
// callback parked, exactly as POST /v1/console/login would return it.
func (s *Server) handleSSOExchange(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry, ok := s.ssoStartByIP.take(ip); !ok {
		writeTooManyRequests(w, retry)
		return
	}
	var req ssoExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	body, ok, err := s.cache.GetDel(r.Context(), ssoCodePrefix+req.Code)
	if err != nil {
		s.log.Warn("sso exchange: read code failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "sso unavailable")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	// A completed login refunds its start and exchange attempts, as password
	// login does, so an office behind one NAT isn't throttled by its own
	// successful sign-ins.
	s.ssoStartByIP.release(ip)
	s.ssoStartByIP.release(ip)
	ssoNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

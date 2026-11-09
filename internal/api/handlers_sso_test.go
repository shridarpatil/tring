package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/secretbox"
)

const ssoTestPublicURL = "https://console.test"

// ssoFlowServer is newSSOTestServer plus a live Redis (state + one-time codes
// live there). Skips when Redis isn't reachable.
// The buffer collects the server's log, where the real failure reason goes.
func ssoFlowServer(t *testing.T, f *dbtest.Fixture) (*Server, *bytes.Buffer) {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
	if err != nil || c == nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return newSSOServerWithCache(t, f, c)
}

func newSSOServerWithCache(t *testing.T, f *dbtest.Fixture, c *cache.Cache) (*Server, *bytes.Buffer) {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{7}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	return NewServer(Config{
		DB:               f.DB,
		JWTSecret:        "test-secret",
		OutboundAllow:    []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		Secrets:          box,
		Cache:            c,
		ConsolePublicURL: ssoTestPublicURL,
		Logger:           slog.New(slog.NewTextHandler(logs, nil)),
	}), logs
}

// seedSSOProvider inserts an enabled provider (client id "cid") owning domains.
func seedSSOProvider(t *testing.T, f *dbtest.Fixture, s *Server, tenantID uuid.UUID, issuer string, requireSSO bool, domains ...string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO sso_providers (tenant_id, name, issuer_url, client_id, client_secret, require_sso, enabled)
		VALUES ($1, 'p', $2, 'cid', $3, $4, true) RETURNING id`,
		tenantID, issuer, s.sealSecret("s3cret"), requireSSO).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, d := range domains {
		if _, err := f.DB.Pool.Exec(ctx,
			`INSERT INTO sso_provider_domains (domain, provider_id, tenant_id) VALUES ($1, $2, $3)`,
			d, id, tenantID); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// addTenant inserts a second tenant (for cross-tenant checks).
func addTenant(t *testing.T, f *dbtest.Fixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	sentinel := "deprecated-" + uuid.NewString()
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ('other', $1, $2, 'deprecated--') RETURNING id`,
		uuid.NewString()+".tring.local", sentinel).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type ssoStart struct {
	Method      string `json:"method"`
	RedirectURL string `json:"redirect_url"`
}

func startSSO(t *testing.T, h http.Handler, email, redirect string) (ssoStart, url.Values) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "redirect": redirect})
	rec := doReq(t, h, "POST", "/v1/console/login/start", "", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("start %s: %d %s", email, rec.Code, rec.Body)
	}
	var st ssoStart
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Method != "sso" {
		return st, nil
	}
	u, err := url.Parse(st.RedirectURL)
	if err != nil {
		t.Fatal(err)
	}
	return st, u.Query()
}

// callbackLocation hits the callback and returns the parsed 302 target.
func callbackLocation(t *testing.T, h http.Handler, state string) *url.URL {
	t.Helper()
	rec := doReq(t, h, "GET", "/v1/console/sso/callback?code=any-code&state="+url.QueryEscape(state), "", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/login" || u.Host != "" {
		t.Fatalf("callback redirected to %s", u)
	}
	return u
}

// assertSSOError checks the generic reason in the redirect and that the log
// carries the specific one, so a test can't pass on an unrelated failure.
func assertSSOError(t *testing.T, logs *bytes.Buffer, loc *url.URL, want, reason string) {
	t.Helper()
	q := loc.Query()
	if q.Get("sso_error") != want || q.Has("sso_code") {
		t.Fatalf("location = %s, want sso_error=%s", loc, want)
	}
	if !strings.Contains(logs.String(), `reason="`+reason+`"`) {
		t.Fatalf("log lacks reason %q:\n%s", reason, logs)
	}
	logs.Reset()
}

func exchangeSSO(t *testing.T, h http.Handler, code string) (int, consoleLoginResponse) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	rec := doReq(t, h, "POST", "/v1/console/sso/exchange", "", string(body))
	var resp consoleLoginResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, resp
}

// ssoLoginTo runs start for startEmail, has the IdP assert idpEmail, and
// returns the callback's redirect target.
func ssoLoginTo(t *testing.T, h http.Handler, idp *fakeIdP, startEmail, idpEmail string) *url.URL {
	t.Helper()
	_, q := startSSO(t, h, startEmail, "")
	if q == nil {
		t.Fatalf("start %s did not route to sso", startEmail)
	}
	idp.setClaims(q.Get("nonce"), idpEmail, true)
	return callbackLocation(t, h, q.Get("state"))
}

func TestSSOStartRoutesByDomain(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	st, q := startSSO(t, h, "Ann@SSO.example.com", "")
	if st.Method != "sso" {
		t.Fatalf("method = %q", st.Method)
	}
	u, _ := url.Parse(st.RedirectURL)
	iu, _ := url.Parse(idp.issuer)
	if u.Host != iu.Host || u.Path != "/authorize" {
		t.Fatalf("redirect_url = %s", st.RedirectURL)
	}
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if len(q.Get(k)) < 40 {
			t.Errorf("%s = %q, want a long random value", k, q.Get(k))
		}
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("login_hint") != "ann@sso.example.com" ||
		q.Get("client_id") != "cid" || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != ssoTestPublicURL+"/v1/console/sso/callback" {
		t.Fatalf("auth params = %v", q)
	}
	scopes := strings.Fields(q.Get("scope"))
	sort.Strings(scopes)
	if strings.Join(scopes, " ") != "email openid" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}

	rec := doReq(t, h, "POST", "/v1/console/login/start", "", `{"email":"bob@other.example.com"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"method":"password"}` {
		t.Fatalf("other domain: %d %s", rec.Code, rec.Body)
	}

	// An unknown user on the SSO domain gets the same shape: start never
	// looks the user up, so it can't be used to enumerate accounts.
	known := doReq(t, h, "POST", "/v1/console/login/start", "", `{"email":"ann@sso.example.com"}`)
	ghost := doReq(t, h, "POST", "/v1/console/login/start", "", `{"email":"ghost@sso.example.com"}`)
	var km, gm map[string]any
	_ = json.Unmarshal(known.Body.Bytes(), &km)
	_ = json.Unmarshal(ghost.Body.Bytes(), &gm)
	if known.Code != ghost.Code || len(km) != len(gm) || km["method"] != gm["method"] || gm["method"] != "sso" {
		t.Fatalf("known %d %s vs unknown %d %s", known.Code, known.Body, ghost.Code, ghost.Body)
	}

	for _, body := range []string{`{}`, `{"email":"  "}`, `{"email":"no-at-sign"}`, `{"email":"a@nodot"}`} {
		if rec := doReq(t, h, "POST", "/v1/console/login/start", "", body); rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "email required") {
			t.Errorf("start %s: %d %s", body, rec.Code, rec.Body)
		}
	}
}

func TestSSOStartNoRedis(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := newSSOServerWithCache(t, f, nil)
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	rec := doReq(t, s.Router(), "POST", "/v1/console/login/start", "", `{"email":"ann@sso.example.com"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"method":"password"}` {
		t.Fatalf("no redis: %d %s", rec.Code, rec.Body)
	}
	// The exchange can't succeed without Redis either.
	if code, _ := exchangeSSO(t, s.Router(), "whatever"); code == http.StatusOK {
		t.Fatal("exchange without redis succeeded")
	}
}

func TestSSOFullFlow(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	id := f.AddUser(t, "supervisor", "ann@sso.example.com")

	_, q := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	loc := callbackLocation(t, h, q.Get("state"))

	// PKCE: the verifier sent to the token endpoint matches the challenge.
	sum := sha256.Sum256([]byte(idp.verifier()))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Fatal("token request verifier does not match the auth request challenge")
	}

	code := loc.Query().Get("sso_code")
	if code == "" || loc.Query().Has("redirect") || loc.Query().Has("sso_error") || len(loc.Query()) != 1 {
		t.Fatalf("location = %s", loc)
	}
	status, resp := exchangeSSO(t, h, code)
	if status != http.StatusOK {
		t.Fatalf("exchange: %d", status)
	}
	claims, err := s.parseConsoleToken(resp.Token)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if claims.Subject != id.String() || claims.TenantID != f.TenantID || claims.Role != "supervisor" {
		t.Fatalf("claims = %+v", claims)
	}
	if resp.User.Email != "ann@sso.example.com" || resp.User.Role != "supervisor" || resp.Tenant.ID != f.TenantID {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSSOStateSingleUse(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	_, q := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	if loc := callbackLocation(t, h, q.Get("state")); !loc.Query().Has("sso_code") {
		t.Fatalf("first callback: %s", loc)
	}
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state")), "expired", "unknown or used state")
	assertSSOError(t, logs, callbackLocation(t, h, "never-issued"), "expired", "unknown or used state")
	assertSSOError(t, logs, callbackLocation(t, h, ""), "expired", "missing state")
}

func TestSSOExchangeSingleUse(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	code := ssoLoginTo(t, h, idp, "ann@sso.example.com", "ann@sso.example.com").Query().Get("sso_code")
	if st, _ := exchangeSSO(t, h, code); st != http.StatusOK {
		t.Fatalf("first exchange: %d", st)
	}
	for _, c := range []string{code, "", "made-up"} {
		body, _ := json.Marshal(map[string]string{"code": c})
		rec := doReq(t, h, "POST", "/v1/console/sso/exchange", "", string(body))
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid or expired code") {
			t.Fatalf("exchange %q: %d %s", c, rec.Code, rec.Body)
		}
	}
}

func TestSSOCallbackBadNonce(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	_, q := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims("some-other-nonce", "ann@sso.example.com", true)
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state")), "failed", "nonce mismatch")
}

func TestSSOCallbackEmailUnverified(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	for _, v := range []any{false, "false", nil, "yes", 1, "TRUE "} {
		_, q := startSSO(t, h, "ann@sso.example.com", "")
		idp.setClaims(q.Get("nonce"), "ann@sso.example.com", v)
		assertSSOError(t, logs, callbackLocation(t, h, q.Get("state")), "failed", "email not verified")
	}
	// Some IdPs send the string "true"; that one counts.
	_, q := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", "true")
	if loc := callbackLocation(t, h, q.Get("state")); !loc.Query().Has("sso_code") {
		t.Fatalf(`email_verified="true": %s`, loc)
	}
}

func TestSSOCallbackEmailOtherDomain(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idpA, idpB := newFakeIdP(t), newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idpA.issuer, false, "a.example.com")
	seedSSOProvider(t, f, s, f.TenantID, idpB.issuer, false, "b.example.com")
	f.AddUser(t, "agent", "ann@a.example.com")
	f.AddUser(t, "agent", "bob@b.example.com")

	// Provider A vouching for a B-domain user (same tenant, user exists).
	assertSSOError(t, logs, ssoLoginTo(t, h, idpA, "ann@a.example.com", "bob@b.example.com"), "failed", "email domain not owned by provider")
	// And for a domain no provider owns.
	f.AddUser(t, "agent", "cat@plain.example.com")
	assertSSOError(t, logs, ssoLoginTo(t, h, idpA, "ann@a.example.com", "cat@plain.example.com"), "failed", "email domain not owned by provider")
}

func TestSSOCallbackUnknownUser(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	assertSSOError(t, logs, ssoLoginTo(t, h, idp, "ghost@sso.example.com", "ghost@sso.example.com"), "no_account", "no user with this email")
}

func TestSSOCallbackTenantMismatch(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	other := addTenant(t, f)
	// The provider belongs to another tenant; the user is in the fixture tenant.
	seedSSOProvider(t, f, s, other, idp.issuer, false, "t.example.com")
	f.AddUser(t, "admin", "ann@t.example.com")
	assertSSOError(t, logs, ssoLoginTo(t, h, idp, "ann@t.example.com", "ann@t.example.com"), "no_account", "user in another tenant")
}

func TestSSOCallbackEmailCaseInsensitive(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	id := f.AddUser(t, "agent", "ann@sso.example.com")

	loc := ssoLoginTo(t, h, idp, "ann@sso.example.com", "  Ann@SSO.Example.COM ")
	st, resp := exchangeSSO(t, h, loc.Query().Get("sso_code"))
	if st != http.StatusOK || resp.User.ID != id.String() || resp.User.Email != "ann@sso.example.com" {
		t.Fatalf("exchange: %d %+v (location %s)", st, resp, loc)
	}
}

func TestSSOCallbackRejectsLookalikeDomain(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")
	// Each of these users exists in the provider's tenant, so only the exact
	// domain check stands between the IdP and a session.
	for _, email := range []string{
		"eve@evilsso.example.com",
		"eve@sub.sso.example.com",
		"eve@sso.example.com.evil.org",
		"eve@sso.example.co",
		`"eve@sso.example.com"@evil.org`,
	} {
		f.AddUser(t, "agent", email)
		assertSSOError(t, logs, ssoLoginTo(t, h, idp, "ann@sso.example.com", email), "failed", "email domain not owned by provider")
	}
}

func TestSSOCallbackProviderDisabledMidFlow(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	pid := seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	_, q := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	if _, err := f.DB.Pool.Exec(context.Background(), `UPDATE sso_providers SET enabled = false WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state")), "failed", "provider disabled")

	// Deleted mid-flow fails the same way.
	if _, err := f.DB.Pool.Exec(context.Background(), `UPDATE sso_providers SET enabled = true WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	_, q = startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	if _, err := f.DB.Pool.Exec(context.Background(), `DELETE FROM sso_providers WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state")), "failed", "provider gone")
}

func TestSSORedirectOnlySameOriginPaths(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	for redirect, want := range map[string]string{
		"/admin":               "/admin",
		"/admin/calls?x=1&y=2": "/admin/calls?x=1&y=2",
		"/agent?tab=live":      "/agent?tab=live",
		"/agent/":              "/agent/",
		"":                     "",
		"/adminx":              "",
		"/login":               "",
		"//evil.example.com":   "",
		"/\\evil.example.com":  "",
		"https://evil.example": "",
		"javascript:alert(1)":  "",
		"admin":                "",
		" /admin":              "",
		"/Admin":               "",
		"/agent@evil.example":  "",
		"/admin\n":             "",
	} {
		_, q := startSSO(t, h, "ann@sso.example.com", redirect)
		idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
		loc := callbackLocation(t, h, q.Get("state"))
		if !loc.Query().Has("sso_code") {
			t.Fatalf("%q: location %s", redirect, loc)
		}
		if got := loc.Query().Get("redirect"); got != want || (want == "" && loc.Query().Has("redirect")) {
			t.Errorf("redirect %q: got %q (location %s), want %q", redirect, got, loc, want)
		}
		// A completed login refunds its attempts, so this loop stays under
		// the per-IP start limit.
		if st, _ := exchangeSSO(t, h, loc.Query().Get("sso_code")); st != http.StatusOK {
			t.Fatalf("exchange: %d", st)
		}
	}
}

func TestPasswordLoginRequireSSO(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := newSSOServerWithCache(t, f, nil)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, true, "req.example.com")
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "opt.example.com")
	for _, u := range []struct{ role, email string }{
		{"supervisor", "sup@req.example.com"},
		{"agent", "agt@req.example.com"},
		{"admin", "adm@req.example.com"},
		{"supervisor", "sup@opt.example.com"},
	} {
		setPassword(t, f, f.AddUser(t, u.role, u.email), "correct-horse")
	}
	login := func(email, pw string) (int, string) {
		body, _ := json.Marshal(map[string]string{"email": email, "password": pw})
		rec := doReq(t, h, "POST", "/v1/console/login", "", string(body))
		return rec.Code, rec.Body.String()
	}

	for _, email := range []string{"sup@req.example.com", "AGT@Req.Example.com"} {
		if code, body := login(email, "correct-horse"); code != http.StatusForbidden ||
			!strings.Contains(body, "this account must sign in with SSO") || strings.Contains(body, "token") {
			t.Errorf("%s: %d %s", email, code, body)
		}
	}
	// A wrong password still answers 401, never revealing the SSO rule.
	if code, _ := login("sup@req.example.com", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", code)
	}
	// Break-glass admin, and a domain without require_sso.
	for _, email := range []string{"adm@req.example.com", "sup@opt.example.com"} {
		if code, body := login(email, "correct-horse"); code != http.StatusOK {
			t.Errorf("%s: %d %s", email, code, body)
		}
	}
	// A disabled require_sso provider no longer blocks password login.
	if _, err := f.DB.Pool.Exec(context.Background(), `UPDATE sso_providers SET enabled = false WHERE require_sso`); err != nil {
		t.Fatal(err)
	}
	if code, body := login("sup@req.example.com", "correct-horse"); code != http.StatusOK {
		t.Errorf("disabled provider: %d %s", code, body)
	}
}

// A require_sso provider in another tenant can't block this tenant's users.
func TestPasswordLoginRequireSSOOtherTenant(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := newSSOServerWithCache(t, f, nil)
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, addTenant(t, f), idp.issuer, true, "x.example.com")
	setPassword(t, f, f.AddUser(t, "agent", "a@x.example.com"), "correct-horse")
	rec := doReq(t, s.Router(), "POST", "/v1/console/login", "", `{"email":"a@x.example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// startSSO returns the start response, the IdP auth URL's query, and the
// tring_sso browser-binding cookie value ("" when not routed to SSO).
func startSSO(t *testing.T, h http.Handler, email, redirect string) (ssoStart, url.Values, string) {
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
		return st, nil, ""
	}
	u, err := url.Parse(st.RedirectURL)
	if err != nil {
		t.Fatal(err)
	}
	return st, u.Query(), ssoCookieFrom(t, rec)
}

func ssoCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == ssoCookieName {
			return c.Value
		}
	}
	t.Fatal("no tring_sso cookie set")
	return ""
}

// doReqCookie is doReq with an optional tring_sso cookie.
func doReqCookie(t *testing.T, h http.Handler, method, path, cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: ssoCookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// ssoFrag is the success redirect's fragment parameters.
func ssoFrag(t *testing.T, loc *url.URL) url.Values {
	t.Helper()
	v, err := url.ParseQuery(loc.EscapedFragment())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// callbackLocation hits the callback (with the binding cookie when non-empty)
// and returns the parsed 302 target.
func callbackLocation(t *testing.T, h http.Handler, state, cookie string) *url.URL {
	t.Helper()
	rec := doReqCookie(t, h, "GET", "/v1/console/sso/callback?code=any-code&state="+url.QueryEscape(state), cookie, "")
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
	if q.Get("sso_error") != want || q.Has("sso_code") || loc.Fragment != "" {
		t.Fatalf("location = %s, want sso_error=%s", loc, want)
	}
	if !strings.Contains(logs.String(), `reason="`+reason+`"`) {
		t.Fatalf("log lacks reason %q:\n%s", reason, logs)
	}
	logs.Reset()
}

func exchangeSSO(t *testing.T, h http.Handler, code, cookie string) (int, consoleLoginResponse) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	rec := doReqCookie(t, h, "POST", "/v1/console/sso/exchange", cookie, string(body))
	var resp consoleLoginResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, resp
}

// ssoLogin runs start for startEmail, has the IdP assert idpEmail, and
// returns the callback's redirect target and the browser-binding cookie.
func ssoLogin(t *testing.T, h http.Handler, idp *fakeIdP, startEmail, idpEmail string) (*url.URL, string) {
	t.Helper()
	_, q, ck := startSSO(t, h, startEmail, "")
	if q == nil {
		t.Fatalf("start %s did not route to sso", startEmail)
	}
	idp.setClaims(q.Get("nonce"), idpEmail, true)
	return callbackLocation(t, h, q.Get("state"), ck), ck
}

func ssoLoginTo(t *testing.T, h http.Handler, idp *fakeIdP, startEmail, idpEmail string) *url.URL {
	t.Helper()
	loc, _ := ssoLogin(t, h, idp, startEmail, idpEmail)
	return loc
}

func TestSSOStartRoutesByDomain(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	st, q, _ := startSSO(t, h, "Ann@SSO.example.com", "")
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
	if code, _ := exchangeSSO(t, s.Router(), "whatever", "any-cookie"); code == http.StatusOK {
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

	_, q, ck := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	loc := callbackLocation(t, h, q.Get("state"), ck)

	// PKCE: the verifier sent to the token endpoint matches the challenge.
	sum := sha256.Sum256([]byte(idp.verifier()))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Fatal("token request verifier does not match the auth request challenge")
	}

	// The code travels in the fragment so it never reaches access logs.
	code := ssoFrag(t, loc).Get("sso_code")
	if code == "" || loc.RawQuery != "" || len(ssoFrag(t, loc)) != 1 || strings.Contains(loc.String(), "?") {
		t.Fatalf("location = %s", loc)
	}
	status, resp := exchangeSSO(t, h, code, ck)
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

	_, q, ck := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	if loc := callbackLocation(t, h, q.Get("state"), ck); !ssoFrag(t, loc).Has("sso_code") {
		t.Fatalf("first callback: %s", loc)
	}
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state"), ck), "expired", "unknown or used state")
	assertSSOError(t, logs, callbackLocation(t, h, "never-issued", ck), "expired", "unknown or used state")
	assertSSOError(t, logs, callbackLocation(t, h, "", ck), "expired", "missing state")
}

func TestSSOExchangeSingleUse(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	loc, ck := ssoLogin(t, h, idp, "ann@sso.example.com", "ann@sso.example.com")
	code := ssoFrag(t, loc).Get("sso_code")
	if st, _ := exchangeSSO(t, h, code, ck); st != http.StatusOK {
		t.Fatalf("first exchange: %d", st)
	}
	for _, c := range []string{code, "", "made-up"} {
		body, _ := json.Marshal(map[string]string{"code": c})
		rec := doReqCookie(t, h, "POST", "/v1/console/sso/exchange", ck, string(body))
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

	_, q, ck := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims("some-other-nonce", "ann@sso.example.com", true)
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state"), ck), "failed", "nonce mismatch")
}

func TestSSOCallbackEmailUnverified(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	for _, v := range []any{false, "false", nil, "yes", 1, "TRUE "} {
		_, q, ck := startSSO(t, h, "ann@sso.example.com", "")
		idp.setClaims(q.Get("nonce"), "ann@sso.example.com", v)
		assertSSOError(t, logs, callbackLocation(t, h, q.Get("state"), ck), "failed", "email not verified")
	}
	// Some IdPs send the string "true"; that one counts.
	_, q, ck := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", "true")
	if loc := callbackLocation(t, h, q.Get("state"), ck); !ssoFrag(t, loc).Has("sso_code") {
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

	loc, ck := ssoLogin(t, h, idp, "ann@sso.example.com", "  Ann@SSO.Example.COM ")
	st, resp := exchangeSSO(t, h, ssoFrag(t, loc).Get("sso_code"), ck)
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

	_, q, ck := startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	if _, err := f.DB.Pool.Exec(context.Background(), `UPDATE sso_providers SET enabled = false WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state"), ck), "failed", "provider disabled")

	// Deleted mid-flow fails the same way.
	if _, err := f.DB.Pool.Exec(context.Background(), `UPDATE sso_providers SET enabled = true WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	_, q, ck = startSSO(t, h, "ann@sso.example.com", "")
	idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
	if _, err := f.DB.Pool.Exec(context.Background(), `DELETE FROM sso_providers WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	assertSSOError(t, logs, callbackLocation(t, h, q.Get("state"), ck), "failed", "provider gone")
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
		_, q, ck := startSSO(t, h, "ann@sso.example.com", redirect)
		idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
		loc := callbackLocation(t, h, q.Get("state"), ck)
		frag := ssoFrag(t, loc)
		if !frag.Has("sso_code") || loc.RawQuery != "" {
			t.Fatalf("%q: location %s", redirect, loc)
		}
		if got := frag.Get("redirect"); got != want || (want == "" && frag.Has("redirect")) {
			t.Errorf("redirect %q: got %q (location %s), want %q", redirect, got, loc, want)
		}
		// A completed login refunds its attempts, so this loop stays under
		// the per-IP start limit.
		if st, _ := exchangeSSO(t, h, frag.Get("sso_code"), ck); st != http.StatusOK {
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

// The callback only works in the browser that started the flow: a callback
// URL sent to someone else (login CSRF) is rejected and the state burned.
func TestSSOCallbackRequiresBrowserBinding(t *testing.T) {
	f := dbtest.Setup(t)
	s, logs := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	body := `{"email":"ann@sso.example.com"}`
	rec := doReq(t, h, "POST", "/v1/console/login/start", "", body)
	var c *http.Cookie
	for _, x := range rec.Result().Cookies() {
		if x.Name == ssoCookieName {
			c = x
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/v1/console/sso" ||
		!c.Secure || c.MaxAge != 600 || len(c.Value) < 40 {
		t.Fatalf("cookie = %+v", c)
	}
	// Password-routed emails get no cookie.
	if rec := doReq(t, h, "POST", "/v1/console/login/start", "", `{"email":"a@other.example.com"}`); len(rec.Result().Cookies()) != 0 {
		t.Fatalf("password route set cookies: %v", rec.Result().Cookies())
	}

	for _, cookie := range []string{"", "someone-elses-cookie"} {
		_, q, ck := startSSO(t, h, "ann@sso.example.com", "")
		idp.setClaims(q.Get("nonce"), "ann@sso.example.com", true)
		assertSSOError(t, logs, callbackLocation(t, h, q.Get("state"), cookie), "expired", "browser binding mismatch")
		// The state was consumed: even the right browser can't use it now.
		assertSSOError(t, logs, callbackLocation(t, h, q.Get("state"), ck), "expired", "unknown or used state")
	}
	// Another flow's genuine cookie doesn't fit this state either.
	_, q1, _ := startSSO(t, h, "ann@sso.example.com", "")
	_, _, ck2 := startSSO(t, h, "ann@sso.example.com", "")
	assertSSOError(t, logs, callbackLocation(t, h, q1.Get("state"), ck2), "expired", "browser binding mismatch")
}

// A one-time code is only redeemable with the cookie of the browser that ran
// the flow, and a wrong attempt burns it.
func TestSSOExchangeRequiresBrowserBinding(t *testing.T) {
	f := dbtest.Setup(t)
	s, _ := ssoFlowServer(t, f)
	h := s.Router()
	idp := newFakeIdP(t)
	seedSSOProvider(t, f, s, f.TenantID, idp.issuer, false, "sso.example.com")
	f.AddUser(t, "agent", "ann@sso.example.com")

	for _, cookie := range []string{"", "someone-elses-cookie"} {
		loc, ck := ssoLogin(t, h, idp, "ann@sso.example.com", "ann@sso.example.com")
		code := ssoFrag(t, loc).Get("sso_code")
		body, _ := json.Marshal(map[string]string{"code": code})
		rec := doReqCookie(t, h, "POST", "/v1/console/sso/exchange", cookie, string(body))
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid or expired code") {
			t.Fatalf("cookie %q: %d %s", cookie, rec.Code, rec.Body)
		}
		if cookie != "" {
			if st, _ := exchangeSSO(t, h, code, ck); st != http.StatusUnauthorized {
				t.Fatalf("code survived a mismatched exchange: %d", st)
			}
		}
	}

	// The right cookie works and is cleared on success.
	loc, ck := ssoLogin(t, h, idp, "ann@sso.example.com", "ann@sso.example.com")
	body, _ := json.Marshal(map[string]string{"code": ssoFrag(t, loc).Get("sso_code")})
	rec := doReqCookie(t, h, "POST", "/v1/console/sso/exchange", ck, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange: %d %s", rec.Code, rec.Body)
	}
	var cleared *http.Cookie
	for _, x := range rec.Result().Cookies() {
		if x.Name == ssoCookieName {
			cleared = x
		}
	}
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Path != "/v1/console/sso" {
		t.Fatalf("cookie not cleared: %+v", cleared)
	}
	var resp consoleLoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Token == "" || strings.Contains(rec.Body.String(), "binding") {
		t.Fatalf("response not the login JSON verbatim: %s", rec.Body)
	}
}

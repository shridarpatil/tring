package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/secretbox"
)

func newSSOTestServer(t *testing.T, f *dbtest.Fixture) *Server {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{7}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(Config{
		DB:            f.DB,
		JWTSecret:     "test-secret",
		OutboundAllow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		Secrets:       box,
	})
}

func ssoBody(issuer, name string, domains ...string) string {
	b, _ := json.Marshal(map[string]any{
		"name": name, "issuer_url": issuer, "client_id": "cid", "client_secret": "s3cret",
		"domains": domains, "require_sso": false, "enabled": true,
	})
	return string(b)
}

func TestSSOProviderCRUD(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSSOTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	idp := newFakeIdP(t)

	rec := doReq(t, h, "POST", "/v1/sso-providers", admin, ssoBody(idp.issuer, "Corp", "Example.com", "corp.example.com"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var p ssoProvider
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.ID.String() == "" {
		t.Fatal(err)
	}
	if len(p.Domains) != 2 || p.Domains[0] != "corp.example.com" || p.Domains[1] != "example.com" {
		t.Fatalf("domains = %v", p.Domains)
	}

	rec = doReq(t, h, "GET", "/v1/sso-providers", admin, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "client_secret") || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list []ssoProvider
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Name != "Corp" {
		t.Fatalf("list = %s", rec.Body)
	}

	var stored string
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT client_secret FROM sso_providers WHERE id = $1`, p.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, secretbox.Prefix) {
		t.Fatalf("secret not sealed: %q", stored)
	}

	rec = doReq(t, h, "GET", "/v1/sso-providers/callback-url", admin, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/v1/console/sso/callback") {
		t.Fatalf("callback-url: %d %s", rec.Code, rec.Body)
	}

	rec = doReq(t, h, "PATCH", "/v1/sso-providers/"+p.ID.String(), admin, `{"name":"Renamed","require_sso":true,"domains":["other.example.org"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	var up ssoProvider
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if up.Name != "Renamed" || !up.RequireSSO || len(up.Domains) != 1 || up.Domains[0] != "other.example.org" {
		t.Fatalf("patched = %+v", up)
	}

	rec = doReq(t, h, "DELETE", "/v1/sso-providers/"+p.ID.String(), admin, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	var n int
	_ = f.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM sso_provider_domains`).Scan(&n)
	if n != 0 {
		t.Fatalf("domains not cascaded: %d", n)
	}
}

func TestSSOProviderValidation(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSSOTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	idp := newFakeIdP(t)
	for name, body := range map[string]string{
		"no domains": ssoBody(idp.issuer, "x"),
		"bad domain": ssoBody(idp.issuer, "x", "a@b.com"),
		"no name":    ssoBody(idp.issuer, "", "a.com"),
		"bad issuer": ssoBody("not a url", "x", "a.com"),
		"no dot":     ssoBody(idp.issuer, "x", "localhost"),
	} {
		if rec := doReq(t, h, "POST", "/v1/sso-providers", admin, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestSSOProviderDomainConflict(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSSOTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	idp := newFakeIdP(t)
	if rec := doReq(t, h, "POST", "/v1/sso-providers", admin, ssoBody(idp.issuer, "A", "dup.example.com")); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	rec := doReq(t, h, "POST", "/v1/sso-providers", admin, ssoBody(idp.issuer, "B", "DUP.example.com"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second: %d %s", rec.Code, rec.Body)
	}
	var n int
	_ = f.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM sso_providers`).Scan(&n)
	if n != 1 {
		t.Fatalf("failed create left a provider row: %d", n)
	}
}

func TestSSOProviderBadIssuer(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSSOTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	// A server with no discovery document.
	dead := newFakeIdP(t)
	rec := doReq(t, h, "POST", "/v1/sso-providers", admin, ssoBody(dead.issuer+"/nope", "A", "bad.example.com"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "issuer discovery failed") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestSSOProviderPatchKeepsSecret(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSSOTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	idp := newFakeIdP(t)
	rec := doReq(t, h, "POST", "/v1/sso-providers", admin, ssoBody(idp.issuer, "A", "keep.example.com"))
	var p ssoProvider
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	read := func() string {
		var v string
		if err := f.DB.Pool.QueryRow(context.Background(),
			`SELECT client_secret FROM sso_providers WHERE id = $1`, p.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
	for _, body := range []string{`{"name":"B"}`, `{"name":"C","client_secret":""}`} {
		if rec := doReq(t, h, "PATCH", "/v1/sso-providers/"+p.ID.String(), admin, body); rec.Code != http.StatusOK {
			t.Fatalf("patch: %d %s", rec.Code, rec.Body)
		}
		if read() != before {
			t.Fatal("secret changed")
		}
	}
	if rec := doReq(t, h, "PATCH", "/v1/sso-providers/"+p.ID.String(), admin, `{"client_secret":"new"}`); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	if read() == before {
		t.Fatal("secret not replaced")
	}
}

func TestSSOProviderNonAdmin(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSSOTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	sup := tokenFor(t, s, f, f.AddUser(t, "supervisor", "sup@example.com"), "sup@example.com", "supervisor")

	rec := doReq(t, h, "POST", "/v1/api-keys", admin, `{"name":"k","scope":"full"}`)
	var k apiKeyResponse
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &k) != nil {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body)
	}
	for _, tok := range []string{sup, k.Key} {
		for _, c := range []struct{ method, path, body string }{
			{"GET", "/v1/sso-providers", ""},
			{"POST", "/v1/sso-providers", `{}`},
			{"GET", "/v1/sso-providers/callback-url", ""},
			{"PATCH", "/v1/sso-providers/" + f.UserID.String(), `{}`},
			{"DELETE", "/v1/sso-providers/" + f.UserID.String(), ""},
		} {
			// An API key has no principal; the existing users tests accept 401 or 403.
			rec := doReq(t, h, c.method, c.path, tok, c.body)
			if rec.Code != http.StatusForbidden && !(tok == k.Key && rec.Code == http.StatusUnauthorized) {
				t.Errorf("%s %s = %d", c.method, c.path, rec.Code)
			}
		}
	}
}

func TestSSOProviderSSOOnlyCount(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSSOTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	idp := newFakeIdP(t)
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO users (tenant_id, email, password_hash, sip_username, sip_password, role)
		VALUES ($1, 'Bob@Count.example.com', NULL, 'bob@count.example.com', 'x', 'agent')`, f.TenantID); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "POST", "/v1/sso-providers", admin, ssoBody(idp.issuer, "A", "count.example.com")); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := doReq(t, h, "GET", "/v1/sso-providers", admin, "")
	var list []ssoProvider
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].SSOOnlyUsers != 1 {
		t.Fatalf("list = %s", rec.Body)
	}
}

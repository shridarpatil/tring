package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/secretbox"
)

func testBox(t *testing.T, b byte) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{b}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func newSealingServer(f *dbtest.Fixture, box *secretbox.Box) *Server {
	return NewServer(Config{
		DB:               f.DB,
		JWTSecret:        "test-secret",
		FSBasicAuth:      "fs:0123456789abcdef",
		GatewayPublicURL: "http://gw.test",
		Secrets:          box,
	})
}

// gatewayDirForTest points the gateway XML writer at a temp dir.
func gatewayDirForTest(t *testing.T) string {
	t.Helper()
	old := freeswitch.GatewayDir
	freeswitch.GatewayDir = t.TempDir()
	t.Cleanup(func() { freeswitch.GatewayDir = old })
	return freeswitch.GatewayDir
}

func rawTrunkPassword(t *testing.T, f *dbtest.Fixture, id string) string {
	t.Helper()
	var pw string
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT sip_password FROM trunks WHERE id = $1`, id).Scan(&pw); err != nil {
		t.Fatalf("read sip_password: %v", err)
	}
	return pw
}

// Create and update store a sealed password; the gateway XML FreeSWITCH reads
// gets the plaintext.
func TestTrunkPasswordSealedAtRestPlaintextInGatewayXML(t *testing.T) {
	f := dbtest.Setup(t)
	box := testBox(t, 1)
	s := newSealingServer(f, box)
	dir := gatewayDirForTest(t)

	// No FreeSWITCH in tests: the rescan fails (502) after the row commits
	// and the XML is written, which is all this test needs.
	rec := serve(t, s, f, http.MethodPost, "/v1/trunks",
		`{"name":"carrier","sip_username":"u1","sip_password":"create-pw","register":true}`)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusBadGateway {
		t.Fatalf("create status = %d, body %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT id FROM trunks WHERE tenant_id = $1 AND name = 'carrier'`, f.TenantID).Scan(&id); err != nil {
		t.Fatalf("trunk not stored: %v", err)
	}

	assertSealed := func(want string) {
		t.Helper()
		raw := rawTrunkPassword(t, f, id)
		if !secretbox.IsSealed(raw) || strings.Contains(raw, want) {
			t.Fatalf("stored sip_password %q is not sealed", raw)
		}
		if got, err := box.Open(raw); err != nil || got != want {
			t.Fatalf("Open(stored) = %q, %v; want %q", got, err, want)
		}
		xml, err := os.ReadFile(filepath.Join(dir, "t_"+id+".xml"))
		if err != nil {
			t.Fatalf("gateway xml: %v", err)
		}
		if !strings.Contains(string(xml), `name="password" value="`+want+`"`) {
			t.Fatalf("gateway xml lacks the plaintext password:\n%s", xml)
		}
		if strings.Contains(string(xml), secretbox.Prefix) {
			t.Fatalf("gateway xml carries the sealed value:\n%s", xml)
		}
	}
	assertSealed("create-pw")

	rec = serve(t, s, f, http.MethodPatch, "/v1/trunks/"+id, `{"sip_password":"rotated-pw"}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadGateway {
		t.Fatalf("patch status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertSealed("rotated-pw")

	// Omitting the password keeps the stored (sealed) one.
	rec = serve(t, s, f, http.MethodPatch, "/v1/trunks/"+id, `{"sip_realm":"realm.example"}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadGateway {
		t.Fatalf("patch status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertSealed("rotated-pw")
}

// A trunk sealed under another key is skipped (logged), not fatal: the rest
// still render.
func TestActiveGatewaysSkipsUndecryptableTrunk(t *testing.T) {
	f := dbtest.Setup(t)
	good := insertRegisterTrunk(t, f, "legacy-plain")
	var bad string
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO trunks (tenant_id, name, sip_username, sip_password, register)
		VALUES ($1, 'other-key', 'u2', $2, true) RETURNING id
	`, f.TenantID, testBox(t, 2).Seal("pw")).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	s := newSealingServer(f, testBox(t, 1))

	gws, err := s.activeGateways(context.Background())
	if err != nil {
		t.Fatalf("activeGateways: %v", err)
	}
	if len(gws) != 1 || gws[0].ID != good || gws[0].Password != "legacy-plain" {
		t.Fatalf("got %+v, want only the legacy-plaintext trunk", gws)
	}
}

func TestAgentPasswordSealedAtRestPlaintextInDirectory(t *testing.T) {
	f := dbtest.Setup(t)
	box := testBox(t, 1)
	s := newSealingServer(f, box)

	rec := serve(t, s, f, http.MethodPost, "/v1/agents", `{"sip_username":"alice"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create agent = %d, body %s", rec.Code, rec.Body.String())
	}
	var raw string
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT sip_password FROM agents WHERE tenant_id = $1 AND sip_username = 'alice'`, f.TenantID,
	).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(raw) {
		t.Fatalf("agent sip_password stored unsealed: %q", raw)
	}
	plain, err := box.Open(raw)
	if err != nil || plain == "" {
		t.Fatalf("Open = %q, %v", plain, err)
	}

	form := url.Values{"section": {"directory"}, "user": {"alice"}, "domain": {f.SIPDomain}}
	req := httptest.NewRequest(http.MethodPost, "/fs/directory", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("fs", "0123456789abcdef")
	out := httptest.NewRecorder()
	s.FSRouter().ServeHTTP(out, req)
	body := out.Body.String()
	if !strings.Contains(body, `value="`+plain+`"`) || strings.Contains(body, secretbox.Prefix) {
		t.Fatalf("directory = %d, want the plaintext password:\n%s", out.Code, body)
	}
}

func TestSealLegacySecretsIsIdempotent(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	box := testBox(t, 1)
	trunkID := insertRegisterTrunk(t, f, "legacy-trunk-pw")
	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO agents (tenant_id, sip_username, sip_password) VALUES ($1, 'bob', 'legacy-agent-pw')
	`, f.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO trunks (tenant_id, name, sip_username, sip_password) VALUES ($1, 'ip-auth', '', '')
	`, f.TenantID); err != nil {
		t.Fatal(err)
	}

	sealed, plain, err := countStoredSecrets(ctx, f.DB)
	if err != nil || sealed != 0 || plain != 2 {
		t.Fatalf("before: sealed=%d plain=%d err=%v; want 0, 2", sealed, plain, err)
	}

	n, err := sealLegacySecrets(ctx, f.DB, box)
	if err != nil || n != 2 {
		t.Fatalf("first pass sealed %d (err %v), want 2", n, err)
	}
	first := rawTrunkPassword(t, f, trunkID)
	if got, err := box.Open(first); err != nil || got != "legacy-trunk-pw" {
		t.Fatalf("trunk after seal: Open = %q, %v", got, err)
	}
	var agentRaw string
	_ = f.DB.Pool.QueryRow(ctx, `SELECT sip_password FROM agents WHERE sip_username = 'bob'`).Scan(&agentRaw)
	if got, err := box.Open(agentRaw); !secretbox.IsSealed(agentRaw) || err != nil || got != "legacy-agent-pw" {
		t.Fatalf("agent after seal: %q → %q, %v", agentRaw, got, err)
	}

	n, err = sealLegacySecrets(ctx, f.DB, box)
	if err != nil || n != 0 {
		t.Fatalf("second pass sealed %d (err %v), want 0", n, err)
	}
	if again := rawTrunkPassword(t, f, trunkID); again != first {
		t.Fatal("second pass re-sealed an already-sealed value")
	}
	sealed, plain, _ = countStoredSecrets(ctx, f.DB)
	if sealed != 2 || plain != 0 {
		t.Fatalf("after: sealed=%d plain=%d; want 2, 0", sealed, plain)
	}
}

// Keyless startup: plaintext rows keep working (upgrade path); sealed rows
// mean the key went missing, so startup must refuse.
func TestPrepareStoredSecretsWithoutKey(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	id := insertRegisterTrunk(t, f, "legacy-pw")

	if err := PrepareStoredSecrets(ctx, f.DB, nil, log); err != nil {
		t.Fatalf("plaintext only, no key: %v", err)
	}
	if raw := rawTrunkPassword(t, f, id); raw != "legacy-pw" {
		t.Fatalf("keyless startup changed the stored value to %q", raw)
	}

	box := testBox(t, 1)
	if err := PrepareStoredSecrets(ctx, f.DB, box, log); err != nil {
		t.Fatalf("with key: %v", err)
	}
	if raw := rawTrunkPassword(t, f, id); !secretbox.IsSealed(raw) {
		t.Fatalf("startup with key left %q unsealed", raw)
	}

	if err := PrepareStoredSecrets(ctx, f.DB, nil, log); err == nil {
		t.Fatal("keyless startup accepted a DB with sealed passwords")
	}
}

func TestOpenSecretWithoutKey(t *testing.T) {
	s := &Server{}
	if got, err := s.openSecret("plain"); err != nil || got != "plain" {
		t.Errorf("openSecret(plain) = %q, %v", got, err)
	}
	if _, err := s.openSecret(testBox(t, 1).Seal("pw")); err == nil {
		t.Error("openSecret(sealed) without a key succeeded")
	}
	if got := s.sealSecret("pw"); got != "pw" {
		t.Errorf("sealSecret without a key = %q, want plaintext", got)
	}
}

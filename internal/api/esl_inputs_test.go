package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func TestValidSipUsername(t *testing.T) {
	for _, s := range []string{"alice", "agent_01", "kalpesh@example.com", "crm-123.4", "a+b"} {
		if !validSipUsername(s) {
			t.Errorf("validSipUsername(%q) = false", s)
		}
	}
	for _, s := range []string{"", "a,b", "a b", "a|b", "a]b", "a=b", "a\nb", "a'b", "a{b}", string(make([]byte, 65))} {
		if validSipUsername(s) {
			t.Errorf("validSipUsername(%q) = true", s)
		}
	}
}

func TestValidFailoverCauses(t *testing.T) {
	if err := validFailoverCauses([]string{"NORMAL_TEMPORARY_FAILURE", "NO_ROUTE_DESTINATION", "503"}); err != nil {
		t.Errorf("valid causes rejected: %v", err)
	}
	for _, bad := range [][]string{{"A,B"}, {"X'}"}, {"a b"}, {""}, {"FOO\n"}} {
		if err := validFailoverCauses(bad); err == nil {
			t.Errorf("validFailoverCauses(%q) = nil", bad)
		}
	}
}

// Values that reach FreeSWITCH dial strings / ESL are rejected at the API.
func TestHandlersRejectESLSyntax(t *testing.T) {
	f := dbtest.Setup(t) // skips unless TRING_TEST_DSN set
	ctx := context.Background()
	s := newTestServer(f)

	send := func(method, target, body string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, method, target, body))
		return rec.Code
	}

	cases := []struct {
		name, method, target, body string
		want                       int
	}{
		{"agent pipe", "POST", "/v1/agents", `{"sip_username":"a|b"}`, http.StatusBadRequest},
		{"agent newline", "POST", "/v1/agents", `{"sip_username":"a\n\napi status"}`, http.StatusBadRequest},
		{"agent ok", "POST", "/v1/agents", `{"sip_username":"kalpesh@example.com"}`, http.StatusCreated},
		{"number comma", "POST", "/v1/numbers", `{"e164":"+91,x=y","type":"inbound"}`, http.StatusBadRequest},
		{"number ok", "POST", "/v1/numbers", `{"e164":"+912265915089","type":"inbound"}`, http.StatusCreated},
		{"causes quote", "PATCH", "/v1/tenants/me", `{"failover_causes":["A','B"]}`, http.StatusBadRequest},
		{"causes ok", "PATCH", "/v1/tenants/me", `{"failover_causes":["NORMAL_TEMPORARY_FAILURE","503"]}`, http.StatusOK},
		{"hold music foreign path", "PATCH", "/v1/tenants/me", `{"hold_music":"/etc/freeswitch/vars.xml"}`, http.StatusBadRequest},
		{"hold music default", "PATCH", "/v1/tenants/me", `{"hold_music":"local_stream://moh"}`, http.StatusOK},
		{"hold music clear", "PATCH", "/v1/tenants/me", `{"hold_music":""}`, http.StatusOK},
	}
	for _, c := range cases {
		if got := send(c.method, c.target, c.body); got != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.target, got, c.want)
		}
	}

	// An uploaded sound of this tenant is accepted as hold music.
	path := f.TenantID.String() + "/moh.ogg"
	if _, err := f.DB.Pool.Exec(ctx,
		`INSERT INTO audio_assets (tenant_id, name, file_path, format, size_bytes) VALUES ($1, 'moh', $2, 'ogg', 1)`,
		f.TenantID, path); err != nil {
		t.Fatalf("insert audio: %v", err)
	}
	if got := send("PATCH", "/v1/tenants/me", `{"hold_music":"`+path+`"}`); got != http.StatusOK {
		t.Errorf("own uploaded hold music = %d, want 200", got)
	}
}

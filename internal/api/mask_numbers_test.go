package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/flowstore"
	"github.com/shridarpatil/tring/internal/mask"
)

const (
	fullFrom   = "+919800000001"
	maskedFrom = "+91••••••0001"
)

func setMaskFlags(t *testing.T, f *dbtest.Fixture, agents, managers, supervisors bool) {
	t.Helper()
	if _, err := f.DB.Pool.Exec(context.Background(), `
		UPDATE tenants SET mask_numbers_agents = $2, mask_numbers_managers = $3, mask_numbers_supervisors = $4
		WHERE id = $1`, f.TenantID, agents, managers, supervisors); err != nil {
		t.Fatalf("set mask flags: %v", err)
	}
}

func maskSettingsFor(flag string) mask.Settings {
	return mask.Settings{Agents: flag == "agent", Managers: flag == "manager", Supervisors: flag == "supervisor"}
}

func TestShouldMask(t *testing.T) {
	p := func(role string, managed ...string) *Principal { return &Principal{Role: role, ManagedTeams: managed} }
	cases := []struct {
		name string
		p    *Principal
	}{
		{"agent", p("agent")},
		{"manager", p("agent", "A")},
		{"supervisor", p("supervisor")},
		{"admin", p("admin")},
	}
	for _, flag := range []string{"agent", "manager", "supervisor"} {
		s := maskSettingsFor(flag)
		for _, c := range cases {
			if got, want := shouldMask(s, c.p), c.name == flag; got != want {
				t.Errorf("flag %s, %s: shouldMask = %v, want %v", flag, c.name, got, want)
			}
		}
	}
	if shouldMask(maskSettingsFor("agent"), nil) {
		t.Error("an API key (no principal) must never be masked")
	}
}

// meCallsFrom returns the from_number of every /v1/me/calls item.
func meCallsFrom(t *testing.T, s *Server, tok string) []string {
	t.Helper()
	rec := doReq(t, s.Router(), "GET", "/v1/me/calls", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("me/calls: %d %s", rec.Code, rec.Body)
	}
	var page struct {
		Items []Call `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range page.Items {
		out = append(out, c.FromNumber)
		if strings.ContainsRune(c.ToNumber, '•') {
			t.Fatalf("to_number %q (the tenant's DID on an inbound call) masked", c.ToNumber)
		}
	}
	return out
}

func dashboardFrom(t *testing.T, s *Server, tok string) []string {
	t.Helper()
	var out []string
	for _, c := range getMeDashboard(t, s, tok).Calls {
		out = append(out, c.FromNumber)
	}
	return out
}

func allEqual(got []string, want string) bool {
	if len(got) == 0 {
		return false
	}
	for _, g := range got {
		if g != want {
			return false
		}
	}
	return true
}

type maskFixture struct {
	s                      *Server
	agentTok, mgrTok, sTok string
}

// newMaskFixture seeds a plain agent and a manager of team A (each with a
// live team call and a finished call of their own) and a supervisor.
func newMaskFixture(t *testing.T) (*dbtest.Fixture, maskFixture) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	a := f.AddUser(t, "agent", "a@example.com")
	m := f.AddUser(t, "agent", "m@example.com")
	sup := f.AddUser(t, "supervisor", "s@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{a: false, m: true})
	insertTeamCall(t, f, dir, a, "A")
	insertTeamCall(t, f, dir, m, "A")
	seedCall(t, f, a, true, todayAt)
	seedCall(t, f, m, true, todayAt)
	seedCall(t, f, sup, true, todayAt)
	return f, maskFixture{
		s:        s,
		agentTok: tokenFor(t, s, f, a, "a@example.com", "agent"),
		mgrTok:   tokenFor(t, s, f, m, "m@example.com", "agent"),
		sTok:     tokenFor(t, s, f, sup, "s@example.com", "supervisor"),
	}
}

func TestMaskNumbersAgentsFlag(t *testing.T) {
	f, mf := newMaskFixture(t)
	setMaskFlags(t, f, true, false, false)

	if got := meCallsFrom(t, mf.s, mf.agentTok); !allEqual(got, maskedFrom) {
		t.Fatalf("agent me/calls from = %v, want masked", got)
	}
	if got := dashboardFrom(t, mf.s, mf.agentTok); !allEqual(got, maskedFrom) {
		t.Fatalf("agent dashboard from = %v, want masked", got)
	}
	for name, tok := range map[string]string{"manager": mf.mgrTok, "supervisor": mf.sTok} {
		if got := meCallsFrom(t, mf.s, tok); !allEqual(got, fullFrom) {
			t.Fatalf("%s me/calls from = %v, want full", name, got)
		}
		if got := dashboardFrom(t, mf.s, tok); !allEqual(got, fullFrom) {
			t.Fatalf("%s dashboard from = %v, want full", name, got)
		}
	}
}

func myTeamCallsFrom(t *testing.T, s *Server, tok string) []string {
	t.Helper()
	rec := doReq(t, s.Router(), "GET", "/v1/my-teams/A", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("my-teams/A: %d %s", rec.Code, rec.Body)
	}
	var mt myTeam
	if err := json.Unmarshal(rec.Body.Bytes(), &mt); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range mt.Calls {
		out = append(out, c.FromNumber)
	}
	return out
}

func TestMaskNumbersManagersFlag(t *testing.T) {
	f, mf := newMaskFixture(t)
	setMaskFlags(t, f, false, true, false)

	if got := meCallsFrom(t, mf.s, mf.mgrTok); !allEqual(got, maskedFrom) {
		t.Fatalf("manager me/calls from = %v, want masked", got)
	}
	if got := dashboardFrom(t, mf.s, mf.mgrTok); !allEqual(got, maskedFrom) {
		t.Fatalf("manager dashboard from = %v, want masked", got)
	}
	if got := myTeamCallsFrom(t, mf.s, mf.mgrTok); !allEqual(got, maskedFrom) {
		t.Fatalf("manager my-teams calls from = %v, want masked", got)
	}
	if got := meCallsFrom(t, mf.s, mf.agentTok); !allEqual(got, fullFrom) {
		t.Fatalf("agent me/calls from = %v, want full", got)
	}
	if got := myTeamCallsFrom(t, mf.s, mf.sTok); !allEqual(got, fullFrom) {
		t.Fatalf("supervisor my-teams calls from = %v, want full", got)
	}
}

// openStream runs the dashboard stream as tok in the background; the returned
// func ends it and returns everything the client received.
func openStream(t *testing.T, s *Server, tok string) func() string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	r := httptest.NewRequest("GET", "/v1/stream/dashboard", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.Router().ServeHTTP(w, r)
		close(done)
	}()
	return func() string {
		cancel()
		<-done
		return w.Body.String()
	}
}

// An admin and a masked supervisor share one tenant stream: one dispatched
// frame reaches the admin byte for byte and the supervisor with the client
// side masked (inbound: the caller and client_ref; the DID stays visible).
func TestMaskNumbersSupervisorsStream(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	hub := events.NewTestHub()
	hub.Forward(events.TypeCallRinging)
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test", RecordingsDir: dir, Hub: hub})
	a := f.AddUser(t, "agent", "a@example.com")
	sup := f.AddUser(t, "supervisor", "s@example.com")
	adm := f.AddUser(t, "admin", "admin@example.com")
	insertTeamCall(t, f, dir, a, "")
	setMaskFlags(t, f, false, false, true)

	endSup := openStream(t, s, tokenFor(t, s, f, sup, "s@example.com", "supervisor"))
	endAdm := openStream(t, s, tokenFor(t, s, f, adm, "admin@example.com", "admin"))
	time.Sleep(300 * time.Millisecond) // both snapshots written, both subscribed
	frame := events.Frame(events.TypeCallRinging, []byte(
		`{"id":"c1","direction":"inbound","from_number":"+919811112222","to_number":"+918000001234","client_ref":"+919811112222","source":"phone"}`))
	hub.DispatchForTest(f.TenantID.String(), frame)
	hub.DispatchForTest(f.TenantID.String(), events.Frame(events.TypeCallRinging, []byte(`not json`)))
	time.Sleep(100 * time.Millisecond)
	supBody, admBody := endSup(), endAdm()

	if !strings.Contains(admBody, string(frame)) {
		t.Fatalf("admin did not get the original frame bytes:\n%s", admBody)
	}
	if !strings.Contains(admBody, fullFrom) || !strings.Contains(admBody, "not json") {
		t.Fatalf("admin stream altered:\n%s", admBody)
	}
	for _, leak := range []string{fullFrom, "+919811112222", "not json"} {
		if strings.Contains(supBody, leak) {
			t.Fatalf("supervisor stream leaks %q:\n%s", leak, supBody)
		}
	}
	for _, want := range []string{maskedFrom, `"from_number":"+91••••••2222"`, `"client_ref":"+91••••••2222"`, `"to_number":"+918000001234"`} {
		if !strings.Contains(supBody, want) {
			t.Fatalf("supervisor stream missing %s:\n%s", want, supBody)
		}
	}
	// The supervisor's REST dashboard is masked too.
	if got := dashboardFrom(t, s, tokenFor(t, s, f, sup, "s@example.com", "supervisor")); !allEqual(got, maskedFrom) {
		t.Fatalf("supervisor dashboard from = %v, want masked", got)
	}
}

// Only the client side is masked: an outbound call's callee (not the tenant's
// caller ID), and a web call's client_ref (the console shows it as the caller).
func TestMaskNumbersClientSideOnly(t *testing.T) {
	f, mf := newMaskFixture(t)
	setMaskFlags(t, f, true, false, false)
	var aID uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE tenant_id = $1 AND sip_username = 'a@example.com'`, f.TenantID).Scan(&aID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, agent_id, started_at, answered_at, ended_at)
		 VALUES ($1, 'outbound', 'ended', '+918000001234', '+919876543210', $2, $3, now(), now(), now())`,
		`INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, agent_id, client_ref, source, started_at, answered_at, ended_at)
		 VALUES ($1, 'inbound', 'ended', '9123456789', 'Support', $2, $3, '9123456789', 'web', now(), now(), now())`,
	} {
		if _, err := f.DB.Pool.Exec(context.Background(), q, f.TenantID, uuid.NewString(), aID); err != nil {
			t.Fatal(err)
		}
	}
	rec := doReq(t, mf.s.Router(), "GET", "/v1/me/calls?limit=100", mf.agentTok, "")
	var page struct {
		Items []Call `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("%d %s: %v", rec.Code, rec.Body, err)
	}
	var sawOut, sawWeb bool
	for _, c := range page.Items {
		switch {
		case c.Direction == "outbound":
			sawOut = true
			if c.FromNumber != "+918000001234" || c.ToNumber != "+91••••••3210" {
				t.Errorf("outbound = %q → %q, want caller ID full, callee masked", c.FromNumber, c.ToNumber)
			}
		case c.ClientRef != nil:
			sawWeb = true
			if *c.ClientRef != "91••••6789" || c.FromNumber != "91••••6789" {
				t.Errorf("web call client_ref/from = %q / %q, want masked", *c.ClientRef, c.FromNumber)
			}
		}
	}
	if !sawOut || !sawWeb {
		t.Fatalf("calls missing: outbound %v web %v (%s)", sawOut, sawWeb, rec.Body)
	}
}

func TestTenantMaskNumbersRoundTrip(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	adm := f.AddUser(t, "admin", "admin@example.com")
	tok := tokenFor(t, s, f, adm, "admin@example.com", "admin")
	get := func() Tenant {
		t.Helper()
		rec := doReq(t, s.Router(), "GET", "/v1/tenants/me", tok, "")
		var out Tenant
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %d %s: %v", rec.Code, rec.Body, err)
		}
		return out
	}
	if got := get(); got.MaskNumbersAgents || got.MaskNumbersManagers || got.MaskNumbersSupervisors {
		t.Fatalf("defaults = %+v, want all false", got)
	}
	rec := doReq(t, s.Router(), "PATCH", "/v1/tenants/me", tok, `{"mask_numbers_agents":true,"mask_numbers_supervisors":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body)
	}
	var out Tenant
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.MaskNumbersAgents || out.MaskNumbersManagers || !out.MaskNumbersSupervisors {
		t.Fatalf("PATCH response = %+v", out)
	}
	if got := get(); !got.MaskNumbersAgents || got.MaskNumbersManagers || !got.MaskNumbersSupervisors {
		t.Fatalf("after PATCH = %+v", got)
	}
	if rec := doReq(t, s.Router(), "PATCH", "/v1/tenants/me", tok, `{"mask_numbers_agents":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH off: %d %s", rec.Code, rec.Body)
	}
	if got := get(); got.MaskNumbersAgents || !got.MaskNumbersSupervisors {
		t.Fatalf("after second PATCH = %+v", got)
	}
}

// The gateway caches each agent's masking verdict inputs; every write that can
// change them (tenant switches, role, manager flag, team membership) drops the
// cached copy so the next ring reads the new verdict.
func TestMaskCacheInvalidation(t *testing.T) {
	f := dbtest.Setup(t)
	s, c := cachedTestServer(t, f)
	ctx := context.Background()
	store := flowstore.New(f.DB).WithCache(c)
	adm := f.AddUser(t, "admin", "admin@example.com")
	a := f.AddUser(t, "agent", "a@example.com")
	x := f.AddUser(t, "agent", "x@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{a: false, x: false})
	f.AddTeam(t, "B", map[uuid.UUID]bool{x: true})
	tok := tokenFor(t, s, f, adm, "admin@example.com", "admin")
	setMaskFlags(t, f, false, true, false) // managers only
	masked := func(sip string) bool {
		t.Helper()
		v, err := store.MaskNumbers(ctx, f.TenantID, sip)
		if err != nil {
			t.Fatalf("MaskNumbers(%s): %v", sip, err)
		}
		return v
	}
	do := func(method, path, body string) {
		t.Helper()
		if rec := doReq(t, s.Router(), method, path, tok, body); rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
	}

	if masked("a@example.com") {
		t.Fatal("plain agent masked under managers-only")
	}
	do("PUT", "/v1/my-teams/A/members/"+a.String()+"/manager", `{"is_manager":true}`)
	if !masked("a@example.com") {
		t.Fatal("promoted manager still served the cached plain-agent verdict")
	}
	do("DELETE", "/v1/teams/A/agents/"+a.String(), "")
	if masked("a@example.com") {
		t.Fatal("removed manager still masked")
	}
	if !masked("x@example.com") {
		t.Fatal("B's manager not masked")
	}
	do("DELETE", "/v1/teams/B", "")
	if masked("x@example.com") {
		t.Fatal("manager of a deleted team still masked")
	}
	do("PATCH", "/v1/users/"+x.String(), `{"role":"supervisor"}`)
	do("PATCH", "/v1/tenants/me", `{"mask_numbers_supervisors":true}`)
	if !masked("x@example.com") {
		t.Fatal("role or tenant switch change not seen through the cache")
	}
}

// maskFrame masks only values that are themselves numbers, and call.transferred's
// target when it is an external number (an agent or team name passes).
func TestMaskFrame(t *testing.T) {
	for _, tc := range []struct{ typ, in, want string }{
		{events.TypeCallTransferred, `{"room":"r","to":"+919833334444"}`, `"to":"+91••••••4444"`},
		{events.TypeCallTransferred, `{"room":"r","to":"agent2"}`, `"to":"agent2"`},
		{events.TypeCallTransferred, `{"room":"r","to":"Sales"}`, `"to":"Sales"`},
		{events.TypeCallRinging, `{"direction":"inbound","client_ref":"Order 2026-11-07"}`, `"client_ref":"Order 2026-11-07"`},
		{events.TypeCallRinging, `{"direction":"inbound","client_ref":"12345"}`, `"client_ref":"12345"`},
		{events.TypeCallRinging, `{"direction":"inbound","from_number":"(022) 2345 6789"}`, `"from_number":"(02•) •••• 6789"`},
	} {
		got := string(maskFrame(events.Frame(tc.typ, []byte(tc.in))))
		if !strings.Contains(got, tc.want) || !strings.HasPrefix(got, "event: "+tc.typ+"\n") {
			t.Errorf("maskFrame(%s %s) = %q, want it to contain %s", tc.typ, tc.in, got, tc.want)
		}
	}
	if maskFrame(events.Frame(events.TypeCallTransferred, []byte("not json"))) != nil {
		t.Error("unparsable transferred frame not dropped")
	}
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// seedCall inserts a call started at the SQL timestamp expression `at` and returns its id.
// agent may be uuid.Nil (unanswered); answered calls get duration 60.
// todayAt is just after the DB's local midnight, so seeded calls are "today"
// whenever the test runs and the window boundary can't race the clock.
const todayAt = "date_trunc('day', now()) + interval '1 second'"

func seedCall(t *testing.T, f *dbtest.Fixture, agent uuid.UUID, answered bool, at string) uuid.UUID {
	t.Helper()
	var agentArg any
	if agent != uuid.Nil {
		agentArg = agent
	}
	var id uuid.UUID
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id,
			agent_id, started_at, answered_at, ended_at, duration_seconds, recording_url)
		VALUES ($1, 'inbound', 'ended', '+919800000001', '+919800000089', $2, $3,
			`+at+`,
			CASE WHEN $4 THEN `+at+` END,
			`+at+` + interval '60 seconds',
			CASE WHEN $4 THEN 60 ELSE 0 END,
			'/v1/calls/x/recording')
		RETURNING id`, f.TenantID, uuid.NewString(), agentArg, answered).Scan(&id)
	if err != nil {
		t.Fatalf("seed call: %v", err)
	}
	return id
}

func seedRing(t *testing.T, f *dbtest.Fixture, call, agent uuid.UUID) {
	t.Helper()
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO call_events (tenant_id, call_id, type, detail)
		VALUES ($1, $2, 'ring', jsonb_build_object('agent_id', $3::text))`,
		f.TenantID, call, agent.String()); err != nil {
		t.Fatalf("seed ring: %v", err)
	}
}

// seedAnswered records that agent joined call, as callcdr.Answered does.
func seedAnswered(t *testing.T, f *dbtest.Fixture, call, agent uuid.UUID) {
	t.Helper()
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO call_events (tenant_id, call_id, type, detail)
		VALUES ($1, $2, 'answered', jsonb_build_object('agent_id', $3::text))`,
		f.TenantID, call, agent.String()); err != nil {
		t.Fatalf("seed answered: %v", err)
	}
}

// seedTransferred seeds a call answered by first and then transferred to
// second: calls.agent_id ends up as second, as callcdr re-stamps it.
func seedTransferred(t *testing.T, f *dbtest.Fixture, first, second uuid.UUID, at string) uuid.UUID {
	t.Helper()
	id := seedCall(t, f, second, true, at)
	seedRing(t, f, id, first)
	seedAnswered(t, f, id, first)
	seedRing(t, f, id, second)
	seedAnswered(t, f, id, second)
	return id
}

func TestMeTokenIsOwnAgentJWT(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id := f.AddUser(t, "agent", "a@example.com")
	tok := tokenFor(t, s, f, id, "a@example.com", "agent")

	rec := doReq(t, s.Router(), "POST", "/v1/me/token", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ParseAgentToken(resp.Token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Subject != id.String() || claims.SipUsername != "a@example.com" {
		t.Fatalf("claims = %+v", claims)
	}
	if resp.GatewayURL != "http://gw.test" {
		t.Fatalf("gateway_url = %q", resp.GatewayURL)
	}
}

func TestMeAvailabilityTogglesOwnOnly(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	me := f.AddUser(t, "agent", "a@example.com")
	other := f.AddUser(t, "agent", "x@example.com")
	tok := tokenFor(t, s, f, me, "a@example.com", "agent")

	rec := doReq(t, s.Router(), "POST", "/v1/me/availability", tok, `{"available":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("availability: %d %s", rec.Code, rec.Body)
	}
	flag := func(id uuid.UUID) (v bool) {
		if err := f.DB.Pool.QueryRow(context.Background(),
			`SELECT available_for_calls FROM users WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if flag(me) {
		t.Fatal("own flag not cleared")
	}
	if !flag(other) {
		t.Fatal("another user's flag changed")
	}
}

func TestMePasswordChange(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id := f.AddUser(t, "agent", "a@example.com")
	setPassword(t, f, id, "old-password")
	tok := tokenFor(t, s, f, id, "a@example.com", "agent")
	h := s.Router()

	if rec := doReq(t, h, "POST", "/v1/me/password", tok, `{"current":"nope","new":"new-password"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current: %d, want 401", rec.Code)
	}
	if rec := doReq(t, h, "POST", "/v1/me/password", tok, `{"current":"old-password","new":"short"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("short new: %d, want 400", rec.Code)
	}
	if rec := doReq(t, h, "POST", "/v1/me/password", tok, `{"current":"old-password","new":"new-password"}`); rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "POST", "/v1/console/login", "", `{"email":"a@example.com","password":"new-password"}`); rec.Code != http.StatusOK {
		t.Fatalf("login with new password: %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "POST", "/v1/console/login", "", `{"email":"a@example.com","password":"old-password"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with old password: %d, want 401", rec.Code)
	}
}

func TestMeStatsTakenAndMissed(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	me := f.AddUser(t, "agent", "a@example.com")
	x := f.AddUser(t, "agent", "x@example.com")
	tok := tokenFor(t, s, f, me, "a@example.com", "agent")

	seedCall(t, f, me, true, todayAt)              // C1: taken by me
	c2 := seedCall(t, f, x, true, todayAt)         // C2: rung me, X answered
	c3 := seedCall(t, f, uuid.Nil, false, todayAt) // C3: rung me, nobody answered
	seedRing(t, f, c2, me)
	seedRing(t, f, c3, me)
	seedRing(t, f, c3, me) // re-ring must not double count
	// Yesterday's call is outside range=today.
	old := seedCall(t, f, me, true, "date_trunc('day', now()) - interval '1 hour'")
	seedRing(t, f, old, me)
	// C4: I answered, then transferred it to X; it stays taken for both.
	seedTransferred(t, f, me, x, todayAt)
	// C5: still ringing me — not missed until it ends unanswered.
	c5 := seedCall(t, f, uuid.Nil, false, todayAt)
	if _, err := f.DB.Pool.Exec(context.Background(),
		`UPDATE calls SET status = 'ringing', ended_at = NULL WHERE id = $1`, c5); err != nil {
		t.Fatal(err)
	}
	seedRing(t, f, c5, me)

	getAs := func(tok, q string) meStats {
		rec := doReq(t, s.Router(), "GET", "/v1/me/stats"+q, tok, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("stats%s: %d %s", q, rec.Code, rec.Body)
		}
		var st meStats
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	get := func(q string) meStats { return getAs(tok, q) }
	// todayAt ago can cross midnight only in the first second of the day;
	// accept that flake window rather than complicate the seed.
	if st := get(""); st.Taken != 2 || st.Missed != 2 || st.TalkSeconds != 120 || st.AvgTalkSeconds != 60 {
		t.Fatalf("today = %+v", st)
	}
	if st := get("?range=7d"); st.Taken != 3 || st.Missed != 2 || st.TalkSeconds != 180 {
		t.Fatalf("7d = %+v", st)
	}
	// X answered C2 and took C4 over from me.
	xTok := tokenFor(t, s, f, x, "x@example.com", "agent")
	if st := getAs(xTok, ""); st.Taken != 2 || st.Missed != 0 {
		t.Fatalf("x today = %+v", st)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/me/stats?range=bogus", tok, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad range: %d", rec.Code)
	}
}

func TestMeCallsOnlyOwn(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test", RecordingsDir: dir})
	me := f.AddUser(t, "agent", "a@example.com")
	x := f.AddUser(t, "agent", "x@example.com")
	tok := tokenFor(t, s, f, me, "a@example.com", "agent")
	mine := seedCall(t, f, me, true, "now() - interval '1 hour'")
	theirs := seedCall(t, f, x, true, "now() - interval '1 hour'")
	// I answered, then transferred to X: it is in both histories.
	handedOff := seedTransferred(t, f, me, x, "now() - interval '2 hours'")

	// Both recordings exist on disk, so a 404 for the other agent's can only
	// come from the ownership check.
	for _, id := range []uuid.UUID{mine, theirs, handedOff} {
		var leg string
		if err := f.DB.Pool.QueryRow(context.Background(), `SELECT leg_id FROM calls WHERE id = $1`, id).Scan(&leg); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, leg+".wav"), []byte("RIFF"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	type callsPage struct {
		Items []Call `json:"items"`
		Total int    `json:"total"`
	}
	list := func(tok string) callsPage {
		rec := doReq(t, s.Router(), "GET", "/v1/me/calls", tok, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("calls: %d %s", rec.Code, rec.Body)
		}
		var page callsPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := list(tok)
	if page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != mine || page.Items[1].ID != handedOff {
		t.Fatalf("page = %+v", page)
	}
	xPage := list(tokenFor(t, s, f, x, "x@example.com", "agent"))
	if xPage.Total != 2 || len(xPage.Items) != 2 || xPage.Items[0].ID != theirs || xPage.Items[1].ID != handedOff {
		t.Fatalf("x page = %+v", xPage)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/me/calls/"+handedOff.String()+"/recording", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("handed-off recording: %d, want 200", rec.Code)
	}
	if got := *page.Items[0].RecordingURL; got != "/v1/me/calls/"+mine.String()+"/recording" {
		t.Fatalf("recording_url = %q", got)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/me/calls/"+mine.String()+"/recording", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("own recording: %d, want 200", rec.Code)
	}
	rec := doReq(t, s.Router(), "GET", "/v1/me/calls/"+theirs.String()+"/recording", tok, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "call not found") {
		t.Fatalf("other's recording: %d %s, want 404 call not found", rec.Code, rec.Body)
	}
}

func TestRequirePrincipalRejectsAPIKey(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	rec := doReq(t, s.Router(), "POST", "/v1/api-keys", tokenFor(t, s, f, f.UserID, f.UserEmail, "admin"), `{"name":"k","scope":"full"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint key: %d %s", rec.Code, rec.Body)
	}
	var k apiKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		t.Fatal(err)
	}
	// An API key has no user, so /v1/me has nothing to act on.
	if rec := doReq(t, s.Router(), "GET", "/v1/me", k.Key, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("api key on /v1/me: %d, want 401", rec.Code)
	}
}

func TestMePasswordReturnsFreshToken(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	id := f.AddUser(t, "agent", "a@example.com")
	setPassword(t, f, id, "old-password")
	old := tokenFor(t, s, f, id, "a@example.com", "agent")

	rec := doReq(t, h, "POST", "/v1/me/password", old, `{"current":"old-password","new":"new-password"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Token == "" || resp.ExpiresAt == "" {
		t.Fatalf("response = %s (%v)", rec.Body, err)
	}
	if rec := doReq(t, h, "GET", "/v1/me", old, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token = %d, want 401", rec.Code)
	}
	if rec := doReq(t, h, "GET", "/v1/me", resp.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("fresh token = %d, want 200", rec.Code)
	}
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// seedCall inserts a call started `ago` before now and returns its id.
// agent may be uuid.Nil (unanswered); answered calls get duration 60.
func seedCall(t *testing.T, f *dbtest.Fixture, agent uuid.UUID, answered bool, ago string) uuid.UUID {
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
			now() - $4::interval,
			CASE WHEN $5 THEN now() - $4::interval END,
			now() - $4::interval + interval '60 seconds',
			CASE WHEN $5 THEN 60 ELSE 0 END,
			'/v1/calls/x/recording')
		RETURNING id`, f.TenantID, uuid.NewString(), agentArg, ago, answered).Scan(&id)
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
	if rec := doReq(t, h, "POST", "/v1/me/password", tok, `{"current":"old-password","new":"new-password"}`); rec.Code != http.StatusNoContent {
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

	seedCall(t, f, me, true, "1 second")              // C1: taken by me
	c2 := seedCall(t, f, x, true, "1 second")         // C2: rung me, X answered
	c3 := seedCall(t, f, uuid.Nil, false, "1 second") // C3: rung me, nobody answered
	seedRing(t, f, c2, me)
	seedRing(t, f, c3, me)
	seedRing(t, f, c3, me) // re-ring must not double count
	// Yesterday's call is outside range=today.
	old := seedCall(t, f, me, true, "2 days")
	seedRing(t, f, old, me)

	get := func(q string) meStats {
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
	// "1 second" ago can cross midnight only in the first second of the day;
	// accept that flake window rather than complicate the seed.
	if st := get(""); st.Taken != 1 || st.Missed != 2 || st.TalkSeconds != 60 || st.AvgTalkSeconds != 60 {
		t.Fatalf("today = %+v", st)
	}
	if st := get("?range=7d"); st.Taken != 2 || st.Missed != 2 || st.TalkSeconds != 120 {
		t.Fatalf("7d = %+v", st)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/me/stats?range=bogus", tok, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad range: %d", rec.Code)
	}
}

func TestMeCallsOnlyOwn(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	me := f.AddUser(t, "agent", "a@example.com")
	x := f.AddUser(t, "agent", "x@example.com")
	tok := tokenFor(t, s, f, me, "a@example.com", "agent")
	mine := seedCall(t, f, me, true, "1 hour")
	theirs := seedCall(t, f, x, true, "1 hour")

	rec := doReq(t, s.Router(), "GET", "/v1/me/calls", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("calls: %d %s", rec.Code, rec.Body)
	}
	var page struct {
		Items []Call `json:"items"`
		Total int    `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != mine {
		t.Fatalf("page = %+v", page)
	}
	if got := *page.Items[0].RecordingURL; got != "/v1/me/calls/"+mine.String()+"/recording" {
		t.Fatalf("recording_url = %q", got)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/me/calls/"+theirs.String()+"/recording", tok, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other's recording: %d, want 404", rec.Code)
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

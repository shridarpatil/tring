package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/webhook"
)

type sentHook struct {
	tenant, id uuid.UUID
	event      string
	data       interface{}
}

type fakeHooks struct {
	mu   sync.Mutex
	sent []sentHook
}

func (h *fakeHooks) Send(tenant, id uuid.UUID, event string, data interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, sentHook{tenant, id, event, data})
}

func (h *fakeHooks) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sent)
}

type wrapFix struct {
	f     *dbtest.Fixture
	s     *Server
	gw    *fakeGatewayControl
	hooks *fakeHooks
	agent uuid.UUID
	tok   string
	hook  uuid.UUID
}

func newWrapFix(t *testing.T) *wrapFix {
	t.Helper()
	f := dbtest.Setup(t)
	gw := &fakeGatewayControl{status: http.StatusNoContent}
	hooks := &fakeHooks{}
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayControlURL: gw.start(t), Webhooks: hooks})
	agent := f.AddUser(t, "agent", "wrap@example.com")
	tok, _, err := s.IssueAgentToken(agent, f.TenantID, "wrap@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &wrapFix{f: f, s: s, gw: gw, hooks: hooks, agent: agent, tok: tok, hook: uuid.New()}
}

func (w *wrapFix) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.f.DB.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (w *wrapFix) label(t *testing.T, table string, tenant uuid.UUID, name string, archived bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	q := `INSERT INTO ` + table + ` (tenant_id, name, archived_at) VALUES ($1, $2, CASE WHEN $3 THEN now() END) RETURNING id`
	if err := w.f.DB.Pool.QueryRow(context.Background(), q, tenant, name, archived).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// call inserts an ended call this agent is wrapping up.
func (w *wrapFix) call(t *testing.T, owner uuid.UUID, require bool, endedAgo time.Duration, wrapEnd string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	w.exec(t, `
		INSERT INTO calls (id, tenant_id, direction, status, from_number, to_number, leg_id, source, agent_id,
		                   answered_at, ended_at, wrap_up_agent_id, wrap_up_require, wrap_up_webhook_id,
		                   wrapped_up_at, wrap_up_end)
		VALUES ($1, $2, 'inbound', 'answered', '+919800000001', 'Support', $3, 'web', $4,
		        now() - interval '3 hours', now() - $5::interval, $4, $6, $7,
		        CASE WHEN $8 = '' THEN NULL ELSE now() END, NULLIF($8, ''))`,
		id, w.f.TenantID, uuid.NewString(), owner, endedAgo.String(), require, w.hook, wrapEnd)
	return id
}

func (w *wrapFix) post(t *testing.T, body string) (int, string) {
	t.Helper()
	rec := doReq(t, w.s.Router(), "POST", "/v1/sdk/calls/wrap-up", w.tok, body)
	return rec.Code, rec.Body.String()
}

func (w *wrapFix) getCall(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	rec := serve(t, w.s, w.f, "GET", "/v1/calls/"+id.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get call: %d %s", rec.Code, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func saveBody(call, disp uuid.UUID, notes string, tags ...uuid.UUID) string {
	ids := []string{}
	for _, tg := range tags {
		ids = append(ids, `"`+tg.String()+`"`)
	}
	d := "null"
	if disp != uuid.Nil {
		d = `"` + disp.String() + `"`
	}
	n, _ := json.Marshal(notes)
	return `{"call_id":"` + call.String() + `","disposition_id":` + d + `,"notes":` + string(n) + `,"tag_ids":[` + strings.Join(ids, ",") + `]}`
}

func TestSaveWrapUp(t *testing.T) {
	w := newWrapFix(t)
	disp := w.label(t, "dispositions", w.f.TenantID, "Resolved", false)
	t1 := w.label(t, "call_tags", w.f.TenantID, "billing", false)
	t2 := w.label(t, "call_tags", w.f.TenantID, "vip", false)
	call := w.call(t, w.agent, false, time.Minute, "")

	if code, body := w.post(t, saveBody(call, disp, "called back", t1, t2)); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	c := w.getCall(t, call)
	if c["wrap_up_end"] != "done" || c["notes"] != "called back" || c["wrapped_up_at"] == nil {
		t.Fatalf("call = %v", c)
	}
	if d := c["disposition"].(map[string]any); d["id"] != disp.String() || d["name"] != "Resolved" {
		t.Fatalf("disposition = %v", d)
	}
	if tags := c["tags"].([]any); len(tags) != 2 {
		t.Fatalf("tags = %v", tags)
	}
	w.gw.mu.Lock()
	if len(w.gw.paths) != 1 || w.gw.paths[0] != "POST /control/wrap-up-done" ||
		w.gw.bodies[0]["sip"] != "wrap@example.com" || w.gw.bodies[0]["call_id"] != call.String() {
		t.Fatalf("gateway saw %v %v", w.gw.paths, w.gw.bodies)
	}
	w.gw.mu.Unlock()
	if w.hooks.count() != 1 || w.hooks.sent[0].event != webhook.EventCallWrappedUp || w.hooks.sent[0].id != w.hook {
		t.Fatalf("webhooks = %+v", w.hooks.sent)
	}
	// A second save is refused: the wrap-up is closed.
	if code, _ := w.post(t, saveBody(call, disp, "again")); code != http.StatusConflict {
		t.Fatalf("second save = %d, want 409", code)
	}
}

func TestSaveRequiresDisposition(t *testing.T) {
	w := newWrapFix(t)
	call := w.call(t, w.agent, true, time.Minute, "")
	if code, body := w.post(t, saveBody(call, uuid.Nil, "x")); code != http.StatusBadRequest {
		t.Fatalf("save: %d %s, want 400", code, body)
	}
	if c := w.getCall(t, call); c["wrapped_up_at"] != nil || c["notes"] != nil {
		t.Fatalf("something was written: %v", c)
	}
}

func TestSaveRejectsForeignIDs(t *testing.T) {
	w := newWrapFix(t)
	other, _, _ := otherTenant(t, w.f)
	live := w.label(t, "dispositions", w.f.TenantID, "Live", false)
	archived := w.label(t, "dispositions", w.f.TenantID, "Old", true)
	okTag := w.label(t, "call_tags", w.f.TenantID, "ok", false)
	foreignTag := w.label(t, "call_tags", other, "theirs", false)
	call := w.call(t, w.agent, false, time.Minute, "")

	if code, _ := w.post(t, saveBody(call, archived, "x")); code != http.StatusBadRequest {
		t.Fatalf("archived disposition = %d, want 400", code)
	}
	if code, _ := w.post(t, saveBody(call, live, "x", okTag, foreignTag)); code != http.StatusBadRequest {
		t.Fatalf("foreign tag = %d, want 400", code)
	}
	var links int
	if err := w.f.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM call_tag_links`).Scan(&links); err != nil || links != 0 {
		t.Fatalf("links = %d (%v), want none written", links, err)
	}
	if c := w.getCall(t, call); c["wrapped_up_at"] != nil {
		t.Fatalf("a rejected save closed the wrap-up: %v", c)
	}
}

func TestSaveLimits(t *testing.T) {
	w := newWrapFix(t)
	call := w.call(t, w.agent, false, time.Minute, "")
	tags := make([]uuid.UUID, 11)
	for i := range tags {
		tags[i] = w.label(t, "call_tags", w.f.TenantID, "t"+string(rune('a'+i)), false)
	}
	if code, _ := w.post(t, saveBody(call, uuid.Nil, "x", tags...)); code != http.StatusBadRequest {
		t.Fatalf("11 tags = %d, want 400", code)
	}
	if code, _ := w.post(t, saveBody(call, uuid.Nil, strings.Repeat("a", 2001))); code != http.StatusBadRequest {
		t.Fatalf("long notes = %d, want 400", code)
	}
	if code, body := w.post(t, saveBody(call, uuid.Nil, strings.Repeat("a", 2000), tags[:10]...)); code != http.StatusOK {
		t.Fatalf("at the limits = %d %s, want 200", code, body)
	}
}

func TestSaveNotMine404(t *testing.T) {
	w := newWrapFix(t)
	other := w.f.AddUser(t, "agent", "other@example.com")
	call := w.call(t, other, false, time.Minute, "")
	if code, _ := w.post(t, saveBody(call, uuid.Nil, "x")); code != http.StatusNotFound {
		t.Fatalf("not mine = %d, want 404", code)
	}
	if code, _ := w.post(t, saveBody(uuid.New(), uuid.Nil, "x")); code != http.StatusNotFound {
		t.Fatalf("unknown call = %d, want 404", code)
	}
}

func TestSaveAfterTimeout409(t *testing.T) {
	w := newWrapFix(t)
	call := w.call(t, w.agent, false, time.Minute, "timeout")
	if code, _ := w.post(t, saveBody(call, uuid.Nil, "x")); code != http.StatusConflict {
		t.Fatalf("after timeout = %d, want 409", code)
	}
}

func TestSaveTooOld409(t *testing.T) {
	w := newWrapFix(t)
	call := w.call(t, w.agent, false, 2*time.Hour, "")
	if code, _ := w.post(t, saveBody(call, uuid.Nil, "x")); code != http.StatusConflict {
		t.Fatalf("too old = %d, want 409", code)
	}
}

func TestSaveGatewayDown(t *testing.T) {
	w := newWrapFix(t)
	w.gw.mu.Lock()
	w.gw.status, w.gw.errMsg = http.StatusBadGateway, "down"
	w.gw.mu.Unlock()
	call := w.call(t, w.agent, false, time.Minute, "")
	if code, body := w.post(t, saveBody(call, uuid.Nil, "kept")); code != http.StatusOK {
		t.Fatalf("gateway down = %d %s, want 200", code, body)
	}
	if c := w.getCall(t, call); c["notes"] != "kept" || c["wrap_up_end"] != "done" {
		t.Fatalf("row not saved: %v", c)
	}
}

func TestPatchWrapUp(t *testing.T) {
	w := newWrapFix(t)
	disp := w.label(t, "dispositions", w.f.TenantID, "Resolved", false)
	tag := w.label(t, "call_tags", w.f.TenantID, "billing", false)
	call := w.call(t, w.agent, false, time.Minute, "timeout")

	rec := serve(t, w.s, w.f, "PATCH", "/v1/calls/"+call.String()+"/wrap-up",
		`{"disposition_id":"`+disp.String()+`","tag_ids":["`+tag.String()+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	c := w.getCall(t, call)
	if c["wrap_up_end"] != "timeout" || c["disposition"] == nil || len(c["tags"].([]any)) != 1 {
		t.Fatalf("call = %v", c)
	}
	if w.hooks.count() != 1 {
		t.Fatalf("webhooks = %d, want 1", w.hooks.count())
	}

	// Notes alone leave disposition and tags; null disposition clears just it.
	if rec := serve(t, w.s, w.f, "PATCH", "/v1/calls/"+call.String()+"/wrap-up", `{"notes":"n"}`); rec.Code != 200 {
		t.Fatalf("notes patch: %d", rec.Code)
	}
	c = w.getCall(t, call)
	if c["notes"] != "n" || c["disposition"] == nil || len(c["tags"].([]any)) != 1 {
		t.Fatalf("absent fields changed: %v", c)
	}
	if rec := serve(t, w.s, w.f, "PATCH", "/v1/calls/"+call.String()+"/wrap-up", `{"disposition_id":null,"tag_ids":[]}`); rec.Code != 200 {
		t.Fatalf("clear patch: %d", rec.Code)
	}
	c = w.getCall(t, call)
	if c["disposition"] != nil || len(c["tags"].([]any)) != 0 || c["notes"] != "n" {
		t.Fatalf("clear failed: %v", c)
	}

	// Foreign tag rejected; unknown call 404.
	other, _, _ := otherTenant(t, w.f)
	foreign := w.label(t, "call_tags", other, "theirs", false)
	if rec := serve(t, w.s, w.f, "PATCH", "/v1/calls/"+call.String()+"/wrap-up", `{"tag_ids":["`+foreign.String()+`"]}`); rec.Code != 400 {
		t.Fatalf("foreign tag patch: %d, want 400", rec.Code)
	}
	if rec := serve(t, w.s, w.f, "PATCH", "/v1/calls/"+uuid.NewString()+"/wrap-up", `{"notes":"n"}`); rec.Code != 404 {
		t.Fatalf("unknown call patch: %d, want 404", rec.Code)
	}
}

func TestListCallsFilters(t *testing.T) {
	w := newWrapFix(t)
	dA := w.label(t, "dispositions", w.f.TenantID, "A", false)
	dB := w.label(t, "dispositions", w.f.TenantID, "B", false)
	tag := w.label(t, "call_tags", w.f.TenantID, "t", false)
	c1 := w.call(t, w.agent, false, time.Minute, "")
	c2 := w.call(t, w.agent, false, time.Minute, "")
	untagged := w.call(t, w.agent, false, time.Minute, "")
	w.exec(t, `UPDATE calls SET disposition_id = $2 WHERE id = $1`, c1, dA)
	w.exec(t, `UPDATE calls SET disposition_id = $2 WHERE id = $1`, c2, dB)
	w.exec(t, `INSERT INTO call_tag_links (tenant_id, call_id, tag_id) VALUES ($1, $2, $3)`, w.f.TenantID, c2, tag)

	list := func(q string) (ids []string, total float64, raw string) {
		rec := serve(t, w.s, w.f, "GET", "/v1/calls"+q, "")
		if rec.Code != 200 {
			t.Fatalf("list %s: %d %s", q, rec.Code, rec.Body)
		}
		var env struct {
			Items []map[string]any `json:"items"`
			Total float64          `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		for _, it := range env.Items {
			ids = append(ids, it["id"].(string))
		}
		return ids, env.Total, rec.Body.String()
	}
	if ids, total, _ := list("?disposition_id=" + dA.String()); len(ids) != 1 || ids[0] != c1.String() || total != 1 {
		t.Fatalf("disposition filter = %v total %v", ids, total)
	}
	if ids, total, _ := list("?tag_id=" + tag.String()); len(ids) != 1 || ids[0] != c2.String() || total != 1 {
		t.Fatalf("tag filter = %v total %v", ids, total)
	}
	ids, total, raw := list("")
	if len(ids) != 3 || total != 3 || strings.Contains(raw, `"tags":null`) || !strings.Contains(raw, `"tags":[]`) {
		t.Fatalf("unfiltered = %v total %v raw %s (untagged %s)", ids, total, raw, untagged)
	}
}

func TestWrapUpOptionsActiveOnly(t *testing.T) {
	w := newWrapFix(t)
	w.label(t, "dispositions", w.f.TenantID, "Live", false)
	w.label(t, "dispositions", w.f.TenantID, "Old", true)
	w.label(t, "call_tags", w.f.TenantID, "live-tag", false)
	w.label(t, "call_tags", w.f.TenantID, "old-tag", true)
	rec := doReq(t, w.s.Router(), "GET", "/v1/sdk/wrap-up-options", w.tok, "")
	if rec.Code != 200 {
		t.Fatalf("options: %d %s", rec.Code, rec.Body)
	}
	var out wrapUpOptions
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Dispositions) != 1 || out.Dispositions[0].Name != "Live" || len(out.Tags) != 1 || out.Tags[0].Name != "live-tag" {
		t.Fatalf("options = %+v", out)
	}
}

func TestPatchDoesNotClobberConcurrentSave(t *testing.T) {
	w := newWrapFix(t)
	disp := w.label(t, "dispositions", w.f.TenantID, "Resolved", false)
	tag := w.label(t, "call_tags", w.f.TenantID, "billing", false)
	oldTag := w.label(t, "call_tags", w.f.TenantID, "old", false)
	call := w.call(t, w.agent, false, time.Minute, "")
	w.exec(t, `INSERT INTO call_tag_links (tenant_id, call_id, tag_id) VALUES ($1, $2, $3)`, w.f.TenantID, call, oldTag)
	// A save that committed after the PATCH handler started.
	w.exec(t, `UPDATE calls SET disposition_id = $2, notes = 'agent notes', wrapped_up_at = now(), wrap_up_end = 'done' WHERE id = $1`, call, disp)

	path := "/v1/calls/" + call.String() + "/wrap-up"
	if rec := serve(t, w.s, w.f, "PATCH", path, `{"tag_ids":["`+tag.String()+`"]}`); rec.Code != 200 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	c := w.getCall(t, call)
	tags := c["tags"].([]any)
	if c["notes"] != "agent notes" || c["disposition"] == nil || len(tags) != 1 || tags[0].(map[string]any)["id"] != tag.String() {
		t.Fatalf("patch clobbered the save: %v", c)
	}

	// notes:null clears notes only.
	if rec := serve(t, w.s, w.f, "PATCH", path, `{"notes":null}`); rec.Code != 200 {
		t.Fatalf("clear notes: %d %s", rec.Code, rec.Body)
	}
	c = w.getCall(t, call)
	if c["notes"] != nil || c["disposition"] == nil || len(c["tags"].([]any)) != 1 {
		t.Fatalf("notes:null changed more than notes: %v", c)
	}
}

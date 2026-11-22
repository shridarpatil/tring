package signaling

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/presence"
	"github.com/shridarpatil/tring/internal/trunkselect"
)

// wrapCallLog is connectCallLog with a call row per room (CallID is Nil for a
// room no call was opened in, as callcdr's is) and recorded wrap-up calls.
type wrapCallLog struct {
	*connectCallLog
	wmu      sync.Mutex
	secs     map[string]int // team → wrap-up seconds; "" is the tenant default
	require  bool
	rooms    map[string]uuid.UUID
	teams    []string      // WrapUpSettings lookups, in order
	begins   []uuid.UUID   // BeginWrapUp call ids
	timeouts []uuid.UUID   // TimeoutWrapUp call ids
	gate     chan struct{} // when set, WrapUpSettings blocks until it is closed
}

func newWrapCallLog(secs map[string]int, require bool) *wrapCallLog {
	return &wrapCallLog{connectCallLog: newConnectCallLog(), secs: secs, require: require, rooms: map[string]uuid.UUID{}}
}

// open gives roomID a call row, as Started does for a real call.
func (w *wrapCallLog) open(roomID string) uuid.UUID {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	id := uuid.New()
	w.rooms[roomID] = id
	return id
}

func (w *wrapCallLog) StartedOutbound(ctx context.Context, tenant uuid.UUID, roomID, from, to, ref string, pid room.ParticipantID) room.TrackSink {
	w.wmu.Lock()
	w.rooms[roomID] = w.id
	w.wmu.Unlock()
	return w.connectCallLog.StartedOutbound(ctx, tenant, roomID, from, to, ref, pid)
}

func (w *wrapCallLog) CallID(roomID string) uuid.UUID {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	return w.rooms[roomID]
}

func (w *wrapCallLog) WrapUpSettings(_ context.Context, _ uuid.UUID, team string) (int, bool, error) {
	w.wmu.Lock()
	w.teams = append(w.teams, team)
	secs, ok := w.secs[team]
	if !ok {
		secs = w.secs[""]
	}
	gate := w.gate
	w.wmu.Unlock()
	if gate != nil {
		<-gate
	}
	return secs, w.require, nil
}

func (w *wrapCallLog) BeginWrapUp(_ context.Context, _, callID uuid.UUID, _ string, _ bool) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	w.begins = append(w.begins, callID)
	return nil
}

func (w *wrapCallLog) TimeoutWrapUp(_ context.Context, _, callID uuid.UUID) (bool, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	w.timeouts = append(w.timeouts, callID)
	return true, nil
}

func (w *wrapCallLog) calls() (teams []string, begins, timeouts []uuid.UUID) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	return append([]string(nil), w.teams...), append([]uuid.UUID(nil), w.begins...), append([]uuid.UUID(nil), w.timeouts...)
}

// wrapHarness is a callHarness on a real presence store whose call log serves
// wrap-up settings, with team membership for team rings.
type wrapHarness struct {
	*callHarness
	wl   *wrapCallLog
	pstr *presence.Store
	sink *captureSink
}

func newWrapHarness(t *testing.T, wl *wrapCallLog, members map[string][]string, results ...connectLeg) *wrapHarness {
	t.Helper()
	pstr := testPresence(t)
	sink := &captureSink{}
	h := &callHarness{t: t, tenant: uuid.New(), o: newConnectOriginator(results...), cl: wl.connectCallLog}
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+912265915000", Name: "t_4cc0a6963ea0"}}}
	h.srv = New(Config{
		SFU: sf, Originator: h.o, Trunks: trunks, Calls: wl, JWTSecret: []byte(testControlSecret),
		Presence: pstr, Events: sink, Teams: queueTeams{members: members},
	})
	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	h.url = strings.Replace(ts.URL, "http://", "ws://", 1)
	wh := &wrapHarness{callHarness: h, wl: wl, pstr: pstr, sink: sink}
	h.pres = wh.online("alice")
	return wh
}

// online registers sip's presence socket and waits until the store has them
// online: ready is sent before that write, and a ring or click-to-call reads it.
func (h *wrapHarness) online(sip string) net.Conn {
	h.t.Helper()
	c := h.register(sip)
	h.waitStatus(sip, presence.StatusOnline)
	return c
}

// register opens sip's presence socket and reads past ready.
func (h *callHarness) register(sip string) net.Conn {
	h.t.Helper()
	c := h.dialAs(sip)
	h.send(c, msg{Type: msgRegister})
	if m := h.read(c); m.Type != msgReady {
		h.t.Fatalf("presence %s: got %+v, want ready", sip, m)
	}
	return c
}

// teamCall puts a caller in a fresh room with a call row and rings team for
// it, as the IVR's Transfer node does. It returns the room and its caller.
func (h *wrapHarness) teamCall(team string) (room.RoomID, *room.Participant, uuid.UUID) {
	h.t.Helper()
	rid := room.RoomID(h.tenant.String() + "/call-" + uuid.NewString())
	caller, err := h.srv.rooms.Join(rid, "caller")
	if err != nil {
		h.t.Fatalf("join caller: %v", err)
	}
	id := h.wl.open(string(rid))
	go func() {
		_, _ = h.srv.agentRouter.RingNoHandoff(context.Background(), string(rid), ivrdrive.Target{Team: team, Label: "Caller"})
	}()
	return rid, caller, id
}

// answerAs answers the ring on sip's presence socket the way the widget does.
func (h *callHarness) answerAs(sip string, pres net.Conn) net.Conn {
	h.t.Helper()
	in := h.readUntil(pres, msgIncoming)
	c := h.dialAs(sip)
	h.send(c, msg{Type: msgJoin, Room: in.Room})
	h.readUntil(c, msgJoined)
	return c
}

func (h *wrapHarness) status(sip string) string {
	return h.pstr.Status(context.Background(), h.tenant.String(), sip)
}

func (h *wrapHarness) waitStatus(sip, want string) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.status(sip) != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s presence = %q, want %q", sip, h.status(sip), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// noWrapUp asserts that c receives no wrap-up message within d.
func noWrapUp(t *testing.T, c net.Conn, d time.Duration) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	for {
		m, err := tryRead(c)
		if err != nil {
			return
		}
		if m.Type == msgWrapUp {
			t.Fatalf("got %+v, want no wrap-up", m)
		}
	}
}

func inWrapUp(s *Server, tenant, sip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrapUps[tenant+"|"+sip] != nil
}

func TestWrapUpStartsAfterAnsweredCall(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"sales": 30}, true)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}})
	_, caller, callID := h.teamCall("sales")
	h.answerAs("alice", h.pres)

	caller.Leave()
	m := h.readUntil(h.pres, msgWrapUp)
	if m.CallID != callID.String() || !m.RequireDisposition {
		t.Fatalf("wrap-up %+v, want call_id %s and require_disposition", m, callID)
	}
	ends, err := time.Parse(time.RFC3339, m.EndsAt)
	if err != nil {
		t.Fatalf("ends_at %q: %v", m.EndsAt, err)
	}
	if d := time.Until(ends); d < 27*time.Second || d > 31*time.Second {
		t.Fatalf("ends_at is %v away, want ≈30s", d)
	}
	if got := h.status("alice"); got != presence.StatusWrapUp {
		t.Fatalf("presence = %q, want wrap_up", got)
	}
	if got := ringAllCandidates(t, h.srv, h.tenant.String()); len(got) != 0 {
		t.Fatalf("ring candidates = %v, want none during wrap-up", got)
	}
	teams, begins, _ := wl.calls()
	if len(teams) != 1 || teams[0] != "sales" {
		t.Fatalf("settings looked up for %v, want [sales]", teams)
	}
	if len(begins) != 1 || begins[0] != callID {
		t.Fatalf("BeginWrapUp calls %v, want [%s]", begins, callID)
	}
}

func TestNoWrapUpWhenOff(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"sales": 0}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}})
	_, caller, _ := h.teamCall("sales")
	h.answerAs("alice", h.pres)

	before := time.Now()
	caller.Leave()
	h.waitStatus("alice", presence.StatusOnline)
	// The online marker is written just before the freed-at stamp.
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		freed, ok := h.pstr.FreedAt(context.Background(), h.tenant.String(), "alice")
		if ok && !freed.Before(before) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("freed-at = %v (ok=%v), want stamped at call end (after %v)", freed, ok, before)
		}
	}
	noWrapUp(t, h.pres, 200*time.Millisecond)
	if _, begins, _ := wl.calls(); len(begins) != 0 {
		t.Fatalf("BeginWrapUp called %v with wrap-up off", begins)
	}
}

// A call that never connected leaves nothing to wrap up: a click-to-call
// whose customer didn't answer, and the agent's own dial that failed.
func TestNoWrapUpWhenNotConnected(t *testing.T) {
	t.Run("click-to-call", func(t *testing.T) {
		wl := newWrapCallLog(map[string]int{"": 30}, false)
		h := newWrapHarness(t, wl, nil, connectLeg{err: errors.New("480 Temporarily Unavailable")})
		h.call()
		agent := h.answer()
		waitEnd(t, h.cl)
		h.readUntil(agent, msgEnded)
		h.waitStatus("alice", presence.StatusOnline)
		noWrapUp(t, h.pres, 200*time.Millisecond)
	})
	t.Run("dial", func(t *testing.T) {
		wl := newWrapCallLog(map[string]int{"": 30}, false)
		h := newWrapHarness(t, wl, nil, connectLeg{err: errors.New("480 Temporarily Unavailable")})
		c := h.dial()
		h.send(c, msg{Type: msgJoin, Room: "r1"})
		h.readUntil(c, msgJoined)
		h.send(c, msg{Type: msgDial, To: "9812345678"})
		h.readUntil(c, msgError)
		h.send(c, msg{Type: msgBye})
		h.waitStatus("alice", presence.StatusOnline)
		noWrapUp(t, h.pres, 200*time.Millisecond)
	})
}

// An agent who hands the call to someone else is done with it: no wrap-up.
func TestTransferAwaySkipsWrapUp(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}, "support": {}})
	_, caller, _ := h.teamCall("sales")
	agent := h.answerAs("alice", h.pres)

	h.send(agent, msg{Type: msgTransfer, ToTeam: "support"})
	h.readUntil(agent, msgEnded)
	h.waitStatus("alice", presence.StatusOnline)
	noWrapUp(t, h.pres, 200*time.Millisecond)
	if inWrapUp(h.srv, h.tenant.String(), "alice") {
		t.Fatal("transferring agent entered wrap-up")
	}
	caller.Leave()
}

// An agent on two legs at once wraps up only when the last one ends, for that
// leg's call.
func TestWrapUpWaitsForLastLeg(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, false)
	h := newWrapHarness(t, wl, nil)
	tenant := h.tenant.String()
	leg := func(name string) (*room.Participant, uuid.UUID) {
		rid := room.RoomID(tenant + "/" + name)
		if _, err := h.srv.rooms.Join(rid, "caller"); err != nil {
			t.Fatal(err)
		}
		p, err := h.srv.rooms.Join(rid, "alice")
		if err != nil {
			t.Fatal(err)
		}
		id := wl.open(string(rid))
		h.srv.holdAgentLeg(p, tenant, "alice", nil, "")
		h.srv.markLegsConnected(rid)
		return p, id
	}
	p1, _ := leg("one")
	p2, id2 := leg("two")

	p1.Leave()
	noWrapUp(t, h.pres, 200*time.Millisecond)
	if got := h.status("alice"); got != presence.StatusOnCall {
		t.Fatalf("presence = %q with a leg still up, want on_call", got)
	}
	p2.Leave()
	if m := h.readUntil(h.pres, msgWrapUp); m.CallID != id2.String() {
		t.Fatalf("wrap-up for %s, want the last leg's call %s", m.CallID, id2)
	}
	h.waitStatus("alice", presence.StatusWrapUp)
}

func TestWrapUpTimerEnds(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 1}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}})
	_, caller, callID := h.teamCall("sales")
	h.answerAs("alice", h.pres)
	caller.Leave()
	m := h.readUntil(h.pres, msgWrapUp)
	ends, _ := time.Parse(time.RFC3339, m.EndsAt)

	if end := h.readUntil(h.pres, msgWrapUpEnd); end.CallID != callID.String() {
		t.Fatalf("wrap-up-end %+v, want call_id %s", end, callID)
	}
	h.waitStatus("alice", presence.StatusOnline)
	// ends_at is truncated to whole seconds, so the exact expiry is at or after it.
	if freed, ok := h.pstr.FreedAt(context.Background(), h.tenant.String(), "alice"); !ok || freed.Before(ends) {
		t.Fatalf("freed-at %v (ok=%v), want at wrap-up expiry %v", freed, ok, ends)
	}
	if _, _, timeouts := wl.calls(); len(timeouts) != 1 || timeouts[0] != callID {
		t.Fatalf("TimeoutWrapUp calls %v, want [%s]", timeouts, callID)
	}
	if got := ringAllCandidates(t, h.srv, h.tenant.String()); len(got) != 1 {
		t.Fatalf("ring candidates = %v, want alice back", got)
	}
}

func TestWrapUpDoneControl(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}})
	_, caller, callID := h.teamCall("sales")
	h.answerAs("alice", h.pres)
	caller.Leave()
	h.readUntil(h.pres, msgWrapUp)

	ctl := h.srv.ControlHandler()
	post := func(tn uuid.UUID, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/control/wrap-up-done", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+controlToken(t, controlAudience, tn, time.Minute))
		rec := httptest.NewRecorder()
		ctl.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(h.tenant, `{"sip":"alice","call_id":"`+uuid.NewString()+`"}`); code != http.StatusNotFound {
		t.Fatalf("wrong call: %d, want 404", code)
	}
	if code := post(uuid.New(), `{"sip":"alice","call_id":"`+callID.String()+`"}`); code != http.StatusNotFound {
		t.Fatalf("other tenant: %d, want 404", code)
	}
	if got := h.status("alice"); got != presence.StatusWrapUp {
		t.Fatalf("presence after a refused done = %q, want wrap_up", got)
	}
	if code := post(h.tenant, `{"sip":"alice","call_id":"`+callID.String()+`"}`); code != http.StatusNoContent {
		t.Fatalf("done: %d, want 204", code)
	}
	h.readUntil(h.pres, msgWrapUpEnd)
	if got := h.status("alice"); got != presence.StatusOnline {
		t.Fatalf("presence after done = %q, want online", got)
	}
	if _, _, timeouts := wl.calls(); len(timeouts) != 0 {
		t.Fatalf("TimeoutWrapUp called %v for a saved wrap-up", timeouts)
	}
	if code := post(h.tenant, `{"sip":"alice","call_id":"`+callID.String()+`"}`); code != http.StatusNotFound {
		t.Fatalf("second done: %d, want 404", code)
	}
}

// A page refresh mid-wrap-up keeps the timer and shows the form again with
// the original deadline.
func TestWrapUpResentOnReconnect(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, true)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}})
	_, caller, callID := h.teamCall("sales")
	h.answerAs("alice", h.pres)
	caller.Leave()
	first := h.readUntil(h.pres, msgWrapUp)

	_ = h.pres.Close()
	h.waitStatus("alice", presence.StatusOffline)
	pres := h.register("alice")
	again := h.readUntil(pres, msgWrapUp)
	if again.CallID != callID.String() || again.EndsAt != first.EndsAt || !again.RequireDisposition {
		t.Fatalf("re-sent wrap-up %+v, want %+v", again, first)
	}
	h.waitStatus("alice", presence.StatusWrapUp)
	if got := ringAllCandidates(t, h.srv, h.tenant.String()); len(got) != 0 {
		t.Fatalf("ring candidates = %v after reconnect, want none during wrap-up", got)
	}
}

// Placing a call ends the agent's wrap-up first (as a timeout), so they are
// never in both states.
func TestDialEndsWrapUp(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}}, connectLeg{err: errors.New("486 Busy")})
	_, caller, callID := h.teamCall("sales")
	h.answerAs("alice", h.pres)
	caller.Leave()
	h.readUntil(h.pres, msgWrapUp)

	c := h.dial()
	h.send(c, msg{Type: msgJoin, Room: "next"})
	h.readUntil(c, msgJoined)
	h.send(c, msg{Type: msgDial, To: "9812345678"})
	select {
	case <-h.o.dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("the dial was never placed")
	}
	if inWrapUp(h.srv, h.tenant.String(), "alice") {
		t.Fatal("wrap-up still active when the call was placed")
	}
	if _, _, timeouts := wl.calls(); len(timeouts) != 1 || timeouts[0] != callID {
		t.Fatalf("TimeoutWrapUp calls %v, want [%s]", timeouts, callID)
	}
	h.readUntil(h.pres, msgWrapUpEnd)
}

// The wrap-up follows the team whose ring reached the agent who ends the
// call, not the team the call first queued in.
func TestTransferredTeamSettingsApply(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"A": 0, "B": 30}, false)
	h := newWrapHarness(t, wl, map[string][]string{"A": {"alice"}, "B": {"bob"}})
	bobPres := h.online("bob")
	_, caller, callID := h.teamCall("A")
	alice := h.answerAs("alice", h.pres)

	h.send(alice, msg{Type: msgTransfer, ToTeam: "B"})
	h.readUntil(alice, msgEnded)
	h.answerAs("bob", bobPres)

	caller.Leave()
	m := h.readUntil(bobPres, msgWrapUp)
	if m.CallID != callID.String() {
		t.Fatalf("wrap-up %+v, want call_id %s", m, callID)
	}
	ends, _ := time.Parse(time.RFC3339, m.EndsAt)
	if d := time.Until(ends); d < 27*time.Second || d > 31*time.Second {
		t.Fatalf("ends_at is %v away, want ≈30s (team B)", d)
	}
	if teams, _, _ := wl.calls(); len(teams) != 1 || teams[0] != "B" {
		t.Fatalf("settings looked up for %v, want [B] (alice transferred away)", teams)
	}
	noWrapUp(t, h.pres, 200*time.Millisecond)
}

// An agent who went Away during wrap-up leaves it as Away: the dashboard reads
// that as online + unavailable, so the last publish must be online (with no
// idle clock restart), never a stale wrap_up.
func TestWrapUpEndWhileAwayPublishesOnline(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}})
	_, caller, callID := h.teamCall("sales")
	h.answerAs("alice", h.pres)
	caller.Leave()
	h.readUntil(h.pres, msgWrapUp)
	go h.srv.PushAvailability(h.tenant.String(), "alice", false)
	h.readUntil(h.pres, msgAvailability)

	if !h.srv.endWrapUp(h.tenant.String(), "alice", callID, false) {
		t.Fatal("wrap-up did not end")
	}
	h.readUntil(h.pres, msgWrapUpEnd)
	h.sink.mu.Lock()
	n := len(h.sink.statuses)
	last, idle := h.sink.statuses[n-1], h.sink.idleSince[len(h.sink.idleSince)-1]
	h.sink.mu.Unlock()
	if last != presence.StatusOnline || idle != nil {
		t.Fatalf("last publish = %q (idle_since %v), want online with no idle_since", last, idle)
	}
	if got := h.status("alice"); got != presence.StatusOnline {
		t.Fatalf("presence = %q, want online", got)
	}
	if got := ringAllCandidates(t, h.srv, h.tenant.String()); len(got) != 0 {
		t.Fatalf("ring candidates = %v, want none while Away", got)
	}
}

// A CRM click-to-call from an agent in wrap-up ends the wrap-up (as a
// timeout) and places the call, instead of refusing them as busy.
func TestClickToCallEndsWrapUp(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}}, newConnectLeg("out-client"))
	_, caller, callID := h.teamCall("sales")
	h.answerAs("alice", h.pres)
	caller.Leave()
	h.readUntil(h.pres, msgWrapUp)

	req := httptest.NewRequest(http.MethodPost, "/control/call", strings.NewReader(`{"agent_sip":"alice","to":"9812345678","agent_label":"Lead 42"}`))
	req.Header.Set("Authorization", "Bearer "+controlToken(t, controlAudience, h.tenant, time.Minute))
	rec := httptest.NewRecorder()
	h.srv.ControlHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("control call: %d %s, want 201", rec.Code, rec.Body)
	}
	if inWrapUp(h.srv, h.tenant.String(), "alice") {
		t.Fatal("wrap-up still active after the call was placed")
	}
	if _, _, timeouts := wl.calls(); len(timeouts) != 1 || timeouts[0] != callID {
		t.Fatalf("TimeoutWrapUp calls %v, want [%s]", timeouts, callID)
	}
	h.readUntil(h.pres, msgWrapUpEnd)
	h.answer() // the call proceeds: alice is rung for it and joins
}

// The wrap-up decision reads the DB, so the call's end must not wait on it:
// the other party is hung up and the call settled while the lookup is still
// out, and meanwhile the agent stays on_call — never ringable — until it lands.
func TestCallEndDoesNotWaitForWrapUpLookup(t *testing.T) {
	wl := newWrapCallLog(map[string]int{"": 30}, false)
	h := newWrapHarness(t, wl, map[string][]string{"sales": {"alice"}})
	_, caller, callID := h.teamCall("sales")
	agent := h.answerAs("alice", h.pres)
	gate := make(chan struct{})
	wl.wmu.Lock()
	wl.gate = gate
	wl.wmu.Unlock()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})

	h.send(agent, msg{Type: msgBye})
	if got := waitEnd(t, h.cl); !strings.HasPrefix(got, "ended:") {
		t.Fatalf("settled %q, want ended", got)
	}
	waitGone(t, h.srv, caller.Room().ID(), caller.ID())
	if !h.srv.agentOnCall(h.tenant.String(), "alice") {
		t.Fatal("agent read as free while the wrap-up decision is pending")
	}
	if got := ringAllCandidates(t, h.srv, h.tenant.String()); len(got) != 0 {
		t.Fatalf("ring candidates = %v while the wrap-up decision is pending, want none", got)
	}
	if got := h.status("alice"); got != presence.StatusOnCall {
		t.Fatalf("presence = %q while the decision is pending, want on_call", got)
	}

	close(gate)
	if m := h.readUntil(h.pres, msgWrapUp); m.CallID != callID.String() {
		t.Fatalf("wrap-up %+v, want call_id %s", m, callID)
	}
	h.waitStatus("alice", presence.StatusWrapUp)
	if h.srv.agentOnCall(h.tenant.String(), "alice") {
		t.Fatal("agent still on a call after entering wrap-up")
	}
}

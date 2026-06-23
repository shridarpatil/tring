package signaling_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pion/rtp"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/media/sfu"
	"github.com/whatomate/tring/internal/media/signaling"
	"github.com/whatomate/tring/internal/routing"
)

// This exercises the signaling protocol wiring — auth, join, room creation, the
// SFU's initial offer, and peer-joined fan-out — WITHOUT requiring ICE/DTLS to
// complete, so it's deterministic (unlike an end-to-end media test).

var testSecret = []byte("test-signaling-secret")

func mintToken(t *testing.T, tenant uuid.UUID, sip string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant":   tenant.String(),
		"sip_user": sip,
		"iss":      "tring",
		"aud":      "tring.sdk",
		"exp":      time.Now().Add(5 * time.Minute).Unix(),
	})
	s, err := tok.SignedString(testSecret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// wsClient is a minimal /rtc client for the test.
type wsClient struct {
	t    *testing.T
	conn net.Conn
}

func dial(t *testing.T, serverURL, token string) *wsClient {
	t.Helper()
	u := strings.Replace(serverURL, "http://", "ws://", 1) + "/rtc?token=" + token
	conn, _, _, err := ws.Dial(context.Background(), u)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &wsClient{t: t, conn: conn}
}

func (c *wsClient) send(m map[string]any) {
	c.t.Helper()
	b, _ := json.Marshal(m)
	if err := wsutil.WriteClientMessage(c.conn, ws.OpText, b); err != nil {
		c.t.Fatalf("ws write: %v", err)
	}
}

// readUntil reads messages until one of the wanted type arrives or it times out.
func (c *wsClient) readUntil(wantType string) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = c.conn.SetReadDeadline(deadline)
		data, err := wsutil.ReadServerText(c.conn)
		if err != nil {
			c.t.Fatalf("waiting for %q: %v", wantType, err)
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m["type"] == wantType {
			return m
		}
	}
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newServerCfg(t, signaling.Config{})
}

// newServerCfg builds a signaling server with the given extra config (SFU +
// secret are filled in).
func newServerCfg(t *testing.T, cfg signaling.Config) *httptest.Server {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	cfg.SFU = s
	cfg.JWTSecret = testSecret
	ts := httptest.NewServer(signaling.New(cfg).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestRejectsMissingToken(t *testing.T) {
	ts := newServer(t)
	u := strings.Replace(ts.URL, "http://", "ws://", 1) + "/rtc"
	_, _, _, err := ws.Dial(context.Background(), u)
	if err == nil {
		t.Fatal("expected dial to fail without a token")
	}
}

func TestJoinReturnsJoinedAndOffer(t *testing.T) {
	ts := newServer(t)
	tenant := uuid.New()
	c := dial(t, ts.URL, mintToken(t, tenant, "a"))
	c.send(map[string]any{"type": "join", "room": "room-1", "name": "A"})

	joined := c.readUntil("joined")
	if joined["participant_id"] == "" || joined["participant_id"] == nil {
		t.Fatalf("joined missing participant_id: %v", joined)
	}
	if joined["session_token"] == "" || joined["session_token"] == nil {
		t.Fatalf("joined missing session_token: %v", joined)
	}
	// The SFU is the sole offerer: the server sends the initial offer right after.
	offer := c.readUntil("offer")
	if sdp, _ := offer["sdp"].(string); !strings.Contains(sdp, "v=0") {
		t.Fatalf("offer missing SDP: %v", offer)
	}
}

func TestSecondJoinFansOutPeerJoined(t *testing.T) {
	ts := newServer(t)
	tenant := uuid.New()

	a := dial(t, ts.URL, mintToken(t, tenant, "a"))
	a.send(map[string]any{"type": "join", "room": "call", "name": "Alice"})
	a.readUntil("joined")

	b := dial(t, ts.URL, mintToken(t, tenant, "b"))
	b.send(map[string]any{"type": "join", "room": "call", "name": "Bob"})
	b.readUntil("joined")

	// A must be told a peer joined, carrying B's name.
	peer := a.readUntil("peer-joined")
	if name, _ := peer["name"].(string); name != "Bob" {
		t.Fatalf("peer-joined name = %q, want Bob (msg=%v)", name, peer)
	}

	// And B, joining second, should have been told Alice is already present (sent
	// inline right after B's joined). Read past offers/ice to find it.
	got := b.readUntil("peer-joined")
	if name, _ := got["name"].(string); name != "Alice" {
		t.Fatalf("B's peer-joined name = %q, want Alice (msg=%v)", name, got)
	}
}

func TestCrossTenantRoomsAreIsolated(t *testing.T) {
	ts := newServer(t)

	a := dial(t, ts.URL, mintToken(t, uuid.New(), "a"))
	a.send(map[string]any{"type": "join", "room": "shared", "name": "Alice"})
	a.readUntil("joined")

	// Different tenant, same room name → a DIFFERENT room (namespaced by tenant),
	// so A must NOT receive a peer-joined for B. Assert by timing out the read.
	b := dial(t, ts.URL, mintToken(t, uuid.New(), "b"))
	b.send(map[string]any{"type": "join", "room": "shared", "name": "Bob"})
	b.readUntil("joined")

	_ = a.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	for {
		data, err := wsutil.ReadServerText(a.conn)
		if err != nil {
			break // timed out with no peer-joined — correct isolation
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m["type"] == "peer-joined" {
			t.Fatal("cross-tenant leak: A saw a peer-joined for another tenant's room")
		}
	}
}

func TestReattachKeepsSameParticipant(t *testing.T) {
	ts := newServer(t)
	tenant := uuid.New()
	tok := mintToken(t, tenant, "a")

	a := dial(t, ts.URL, tok)
	a.send(map[string]any{"type": "join", "room": "call", "name": "A"})
	joined1 := a.readUntil("joined")
	pid1, _ := joined1["participant_id"].(string)
	token, _ := joined1["session_token"].(string)
	if pid1 == "" || token == "" {
		t.Fatalf("missing pid/token: %v", joined1)
	}

	// Simulate a hard refresh: the socket drops...
	_ = a.conn.Close()
	// ...and the client reconnects within the grace window, presenting the token.
	a2 := dial(t, ts.URL, tok)
	a2.send(map[string]any{"type": "join", "room": "call", "name": "A", "session_token": token})
	joined2 := a2.readUntil("joined")
	pid2, _ := joined2["participant_id"].(string)
	if pid2 != pid1 {
		t.Fatalf("reattach gave a new participant %q, want same slot %q", pid2, pid1)
	}
}

func TestPeerSeesReconnectingOnDrop(t *testing.T) {
	ts := newServer(t)
	tenant := uuid.New()

	a := dial(t, ts.URL, mintToken(t, tenant, "a"))
	a.send(map[string]any{"type": "join", "room": "call", "name": "Alice"})
	a.readUntil("joined")

	b := dial(t, ts.URL, mintToken(t, tenant, "b"))
	b.send(map[string]any{"type": "join", "room": "call", "name": "Bob"})
	b.readUntil("joined")
	a.readUntil("peer-joined") // A learns about B

	// B refreshes (socket drops) → A must be told B is reconnecting (grace window),
	// NOT that B left. (Skip past B's earlier `joining` peer-state.)
	_ = b.conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("A never saw B reconnecting")
		}
		st := a.readUntil("peer-state")
		if st["state"] == "reconnecting" {
			break
		}
		if st["state"] == "left" {
			t.Fatal("A saw B left, want reconnecting (grace window)")
		}
	}
}

// transferFlows is a fake FlowLoader returning start → transfer(team=sales).
type transferFlows struct{}

func (transferFlows) Load(_ context.Context, _ string) (*ivr.Flow, error) {
	return &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "x", Type: ivr.NodeTransfer, Config: map[string]any{"team": "sales"}},
		},
		Edges: []ivr.Edge{{From: "s", To: "x", Condition: "default"}},
	}, nil
}

func (transferFlows) FlowName(_ context.Context, _ string) (string, error) { return "Sales", nil }

// TestTransferRingsAgentIntoRoom is the M9 membership-change end-to-end (minus
// real media): a visitor enters a flow-pinned room whose IVR immediately
// transfers; an online agent is rung, joins the room, and the visitor sees the
// agent as a peer. No FreeSWITCH involved.
func TestTransferRingsAgentIntoRoom(t *testing.T) {
	ts := newServerCfg(t, signaling.Config{Flows: transferFlows{}})
	tenant := uuid.New()

	// Agent registers presence and waits for a ring.
	agent := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	agent.send(map[string]any{"type": "register"})
	agent.readUntil("ready")

	// Visitor joins the flow-pinned room → IVR spawns → transfers → rings agents.
	visitor := dial(t, ts.URL, mintToken(t, tenant, "visitor"))
	visitor.send(map[string]any{"type": "join", "room": "call", "flow_id": "f1", "name": "Caller"})
	visitor.readUntil("joined")

	// Agent is rung for the same room.
	inc := agent.readUntil("incoming")
	if inc["room"] != "call" {
		t.Fatalf("incoming room = %v, want call", inc["room"])
	}

	// Agent answers by joining the room → the transfer completes and the visitor
	// sees the agent join as a peer.
	answer := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	answer.send(map[string]any{"type": "join", "room": "call", "name": "Alice"})
	answer.readUntil("joined")

	pj := visitor.readUntil("peer-joined")
	if pj["name"] != "Alice" {
		t.Fatalf("visitor peer-joined name = %v, want Alice", pj["name"])
	}
}

// fakeTeams resolves a fixed team membership (no DB).
type fakeTeams struct{ members map[string][]string }

func (f fakeTeams) TeamSIPs(_ context.Context, _ uuid.UUID, team string) ([]string, error) {
	return f.members[team], nil
}

// TeamConfig: a team exists (with default queue settings) iff it has members.
func (f fakeTeams) TeamConfig(_ context.Context, _ uuid.UUID, team string) (routing.TeamConfig, bool, error) {
	if _, ok := f.members[team]; !ok {
		return routing.TeamConfig{}, false, nil
	}
	return routing.TeamConfig{Name: team}, true, nil
}

// TestTransferRingsOnlyTeamMembers checks team-resolution parity: a transfer to
// team "sales" rings only the team's online members, not every online agent.
func TestTransferRingsOnlyTeamMembers(t *testing.T) {
	ts := newServerCfg(t, signaling.Config{
		Flows: transferFlows{},
		Teams: fakeTeams{members: map[string][]string{"sales": {"alice"}}}, // bob is NOT in sales
	})
	tenant := uuid.New()

	alice := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	alice.send(map[string]any{"type": "register"})
	alice.readUntil("ready")

	bob := dial(t, ts.URL, mintToken(t, tenant, "bob"))
	bob.send(map[string]any{"type": "register"})
	bob.readUntil("ready")

	visitor := dial(t, ts.URL, mintToken(t, tenant, "visitor"))
	visitor.send(map[string]any{"type": "join", "room": "call", "flow_id": "f1", "name": "Caller"})
	visitor.readUntil("joined")

	// alice (in sales) is rung.
	if inc := alice.readUntil("incoming"); inc["room"] != "call" {
		t.Fatalf("alice incoming room = %v, want call", inc["room"])
	}
	// bob (not in sales) must NOT be rung — assert by a short read that times out.
	_ = bob.conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	for {
		data, err := wsutil.ReadServerText(bob.conn)
		if err != nil {
			break // timed out with no incoming — correct
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m["type"] == "incoming" {
			t.Fatal("bob was rung but is not in team sales")
		}
	}
}

// TestTransferSkipsBusyAgentAndQueues verifies the two routing fixes together:
// a transfer must NOT ring an agent who is already on a call (no second
// incoming-call popup), and the caller waits in queue until that agent frees up
// — then gets rung — instead of immediately failing over to no_answer.
func TestTransferSkipsBusyAgentAndQueues(t *testing.T) {
	ts := newServerCfg(t, signaling.Config{
		Flows: transferFlows{},
		Teams: fakeTeams{members: map[string][]string{"sales": {"alice"}}},
	})
	tenant := uuid.New()

	// alice holds a presence socket AND is already on a call (a second socket
	// joined another room → she's on_call).
	alice := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	alice.send(map[string]any{"type": "register"})
	alice.readUntil("ready")

	busy := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	busy.send(map[string]any{"type": "join", "room": "busyroom", "name": "Alice"})
	busy.readUntil("joined") // alice is now on_call

	// A visitor enters the flow → transfer to team sales (only member: alice).
	visitor := dial(t, ts.URL, mintToken(t, tenant, "visitor"))
	visitor.send(map[string]any{"type": "join", "room": "call", "flow_id": "f1", "name": "Caller"})
	visitor.readUntil("joined")

	// alice must NOT be rung while busy — the caller is queued instead.
	_ = alice.conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
	for {
		data, err := wsutil.ReadServerText(alice.conn)
		if err != nil {
			break // timed out with no incoming — correct (busy agent skipped, caller queued)
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m["type"] == "incoming" {
			t.Fatal("busy agent was rung; should have been skipped and the caller queued")
		}
	}

	// When alice frees up (leaves the other call), the queued caller rings her.
	_ = busy.conn.Close()
	if inc := alice.readUntil("incoming"); inc["room"] != "call" {
		t.Fatalf("freed agent not rung for the queued caller; incoming = %v", inc)
	}
}

// TestDevFlowRingsAgentWithoutDB checks the self-contained demo path: with dev
// tokens on and NO FlowLoader, joining with flow_id "dev" runs the built-in
// transfer flow and rings an online agent.
func TestDevFlowRingsAgentWithoutDB(t *testing.T) {
	ts := newServerCfg(t, signaling.Config{DevTokens: true}) // Flows nil
	tenant := uuid.New()

	agent := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	agent.send(map[string]any{"type": "register"})
	agent.readUntil("ready")

	visitor := dial(t, ts.URL, mintToken(t, tenant, "visitor"))
	visitor.send(map[string]any{"type": "join", "room": "call", "flow_id": "dev", "name": "Caller"})
	visitor.readUntil("joined")

	if inc := agent.readUntil("incoming"); inc["room"] != "call" {
		t.Fatalf("agent incoming room = %v, want call", inc["room"])
	}
}

// fakeControl is a connected controlChannel stand-in (its Send is a no-op).
type fakeControl struct{}

func (fakeControl) Send(control.Message) error { return nil }
func (fakeControl) Connected() bool            { return true }

// TestEdgeRingCommandRingsAgent checks Phase 2c's edge side: the brain already
// resolved WHICH agent, so a `ring` command with a specific sip just pushes the
// `incoming` ring to that agent's socket — no routing on the edge.
func TestEdgeRingCommandRingsAgent(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu: %v", err)
	}
	sig := signaling.New(signaling.Config{SFU: s, JWTSecret: testSecret})
	ts := httptest.NewServer(sig.Handler())
	t.Cleanup(ts.Close)
	sig.SetControl(fakeControl{})

	tenant := uuid.New()
	agent := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	agent.send(map[string]any{"type": "register"})
	agent.readUntil("ready")

	// Brain-resolved targeted ring → edge pushes incoming to alice (non-blocking).
	sig.HandleControlCommand(control.Message{
		Type: control.CmdRing, Room: tenant.String() + "/call", Sip: "alice", From: "Caller",
	})

	if inc := agent.readUntil("incoming"); inc["room"] != "call" {
		t.Fatalf("incoming room = %v, want call", inc["room"])
	}
}

// ── Phase 4c: inbound phone call via the PSTN plug ──────────────────

// blockingSource is a ParticipantSource that yields nothing until closed (a
// silent phone caller — keeps the port attached without media in tests).
type blockingSource struct {
	id   room.ParticipantID
	stop chan struct{}
	once sync.Once
}

func (b *blockingSource) ID() room.ParticipantID { return b.id }
func (b *blockingSource) ReadRTP() (*rtp.Packet, error) {
	<-b.stop
	return nil, errSourceClosed
}
func (b *blockingSource) Close() error {
	b.once.Do(func() { close(b.stop) })
	return nil
}

var errSourceClosed = errors.New("closed")

// fakePstn is a scripted phone plug (the FS adapter stand-in).
type fakePstn struct {
	inbound  func(room.InboundCall)
	src      *blockingSource
	dtmf     chan string
	done     chan struct{}
	hangups  chan room.CallID
	rejected chan room.CallID
}

func newFakePstn() *fakePstn {
	return &fakePstn{
		src:      &blockingSource{id: "pstn-leg", stop: make(chan struct{})},
		dtmf:     make(chan string, 4),
		done:     make(chan struct{}),
		hangups:  make(chan room.CallID, 2),
		rejected: make(chan room.CallID, 2),
	}
}

func (f *fakePstn) OnInbound(fn func(room.InboundCall)) { f.inbound = fn }
func (f *fakePstn) Answer(room.CallID) (*room.PstnLeg, error) {
	return &room.PstnLeg{
		Source: f.src,
		Write:  func(*rtp.Packet) error { return nil },
		DTMF:   f.dtmf,
		Done:   f.done,
	}, nil
}
func (f *fakePstn) Reject(id room.CallID) error { f.rejected <- id; return nil }
func (f *fakePstn) Hangup(id room.CallID) error {
	f.hangups <- id
	f.src.Close()
	close(f.done) // a real adapter fires Done after BYE
	return nil
}

type fakeDIDs struct {
	tenant uuid.UUID
	flow   string
}

func (f fakeDIDs) ResolveDID(_ context.Context, _ string) (uuid.UUID, string, error) {
	return f.tenant, f.flow, nil
}

// TestInboundPhoneCallRingsAgentAndEndsOnHangup is the 4c end-to-end (no SIP,
// no media): a phone call arrives via the plug → the IVR transfers → an agent
// is rung and joins → the carrier hangs up → the agent's call is ended.
func TestInboundPhoneCallRingsAgentAndEndsOnHangup(t *testing.T) {
	tenant := uuid.New()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu: %v", err)
	}
	sig := signaling.New(signaling.Config{SFU: s, JWTSecret: testSecret, Flows: transferFlows{}})
	ts := httptest.NewServer(sig.Handler())
	t.Cleanup(ts.Close)
	fp := newFakePstn()
	sig.AttachPstn(fp, fakeDIDs{tenant: tenant, flow: "f1"})

	agent := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	agent.send(map[string]any{"type": "register"})
	agent.readUntil("ready")

	// The carrier delivers a call (the adapter raises OnInbound).
	fp.inbound(room.InboundCall{CallID: "c1", From: "+919900112233", To: "+916600112233"})

	// IVR runs → transfer → alice is rung; the room is the pstn call's room.
	inc := agent.readUntil("incoming")
	if inc["room"] != "pstn-c1" {
		t.Fatalf("incoming room = %v, want pstn-c1", inc["room"])
	}
	if inc["from"] != "+919900112233" {
		t.Fatalf("incoming from = %v, want the caller id", inc["from"])
	}

	// Agent answers by joining the pstn room.
	ans := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	ans.send(map[string]any{"type": "join", "room": "pstn-c1", "name": "Alice"})
	ans.readUntil("joined")

	// Carrier hangs up → the leg's Done fires → the agent gets a terminal ended.
	fp.src.Close()
	close(fp.done)
	if end := ans.readUntil("ended"); end["reason"] != "caller hung up" {
		t.Fatalf("ended reason = %v", end["reason"])
	}
}

// TestInboundPhoneCallWithoutFlowIsRejected: a DID with no flow attached can't
// be served — the plug must reject so the carrier hears busy, not dead air.
func TestInboundPhoneCallWithoutFlowIsRejected(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu: %v", err)
	}
	sig := signaling.New(signaling.Config{SFU: s, JWTSecret: testSecret})
	fp := newFakePstn()
	sig.AttachPstn(fp, fakeDIDs{tenant: uuid.New(), flow: ""})

	fp.inbound(room.InboundCall{CallID: "c2", From: "+91", To: "+1600"})
	select {
	case id := <-fp.rejected:
		if id != "c2" {
			t.Fatalf("rejected %q, want c2", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flowless DID was not rejected")
	}
}

// fakeCallLog records CDR lifecycle hooks.
type fakeCallLog struct {
	mu       sync.Mutex
	started  []string // "room|from|to"
	answered []string // "room|pid"
	ended    []string // "room|cause"
}

func (f *fakeCallLog) Started(_ context.Context, _ uuid.UUID, roomID, from, to, source, _ string, _ room.ParticipantID) room.TrackSink {
	f.mu.Lock()
	f.started = append(f.started, roomID+"|"+from+"|"+to+"|"+source)
	f.mu.Unlock()
	return nil
}
func (f *fakeCallLog) Answered(roomID string, pid room.ParticipantID, agentSip string) room.TrackSink {
	f.mu.Lock()
	f.answered = append(f.answered, roomID+"|"+string(pid)+"|"+agentSip)
	f.mu.Unlock()
	return nil
}
func (f *fakeCallLog) Journey(string, []ivr.Step) {}
func (f *fakeCallLog) Ended(roomID, cause string) {
	f.mu.Lock()
	f.ended = append(f.ended, roomID+"|"+cause)
	f.mu.Unlock()
}
func (f *fakeCallLog) snapshot() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started), len(f.answered), len(f.ended)
}

// TestPstnCallWritesCDRHooks: a phone call opens a CDR, an agent joining marks
// it answered, and the carrier hangup ends it.
func TestPstnCallWritesCDRHooks(t *testing.T) {
	tenant := uuid.New()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu: %v", err)
	}
	cl := &fakeCallLog{}
	sig := signaling.New(signaling.Config{SFU: s, JWTSecret: testSecret, Flows: transferFlows{}, Calls: cl})
	ts := httptest.NewServer(sig.Handler())
	t.Cleanup(ts.Close)
	fp := newFakePstn()
	sig.AttachPstn(fp, fakeDIDs{tenant: tenant, flow: "f1"})

	agent := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	agent.send(map[string]any{"type": "register"})
	agent.readUntil("ready")

	fp.inbound(room.InboundCall{CallID: "c9", From: "+9199", To: "+1600"})
	agent.readUntil("incoming")
	if n, _, _ := cl.snapshot(); n != 1 {
		t.Fatalf("started hooks = %d, want 1", n)
	}

	ans := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	ans.send(map[string]any{"type": "join", "room": "pstn-c9", "name": "Alice"})
	ans.readUntil("joined")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, a, _ := cl.snapshot(); a >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, a, _ := cl.snapshot(); a < 1 {
		t.Fatal("agent join did not mark the call answered")
	}

	fp.src.Close()
	close(fp.done)
	ans.readUntil("ended")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, e := cl.snapshot(); e >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, e := cl.snapshot(); e < 1 {
		t.Fatal("carrier hangup did not end the CDR")
	}
}

// TestPeerLeaveEndsLoneRemaining: in a 1:1 room, when one party leaves (Bye),
// the other leg is ended PROMPTLY rather than lingering in the 30s grace window.
// The lingering case was the stale dashboard "on call" count — the room stayed
// non-empty (so the CDR never settled) until grace expired. readUntil's 5s
// deadline is well under the 30s grace, so this fails if the leg isn't ended now.
func TestPeerLeaveEndsLoneRemaining(t *testing.T) {
	tenant := uuid.New()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu: %v", err)
	}
	sig := signaling.New(signaling.Config{SFU: s, JWTSecret: testSecret})
	ts := httptest.NewServer(sig.Handler())
	t.Cleanup(ts.Close)

	a := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	a.send(map[string]any{"type": "join", "room": "r1", "name": "A"})
	a.readUntil("joined")

	b := dial(t, ts.URL, mintToken(t, tenant, "bob"))
	b.send(map[string]any{"type": "join", "room": "r1", "name": "B"})
	b.readUntil("joined")

	// A hangs up cleanly (Bye → immediate Leave). B is now the lone remaining leg
	// and must be ended now (server-side endIfAlone), not after the grace window.
	a.send(map[string]any{"type": "bye"})
	if end := b.readUntil("ended"); end["type"] != "ended" {
		t.Fatalf("remaining leg not ended after peer left: %v", end)
	}
}

// orderedCallLog records the ORDER of Journey vs Ended (and the stamped steps)
// so a test can assert the abandoned-call fix: the IVR journey must be stamped
// BEFORE the CDR settles, because Ended() drops the active-call tracking that
// Journey() needs. Regression guard for the stopIVR→Journey→Ended race that left
// abandoned web calls with an empty ivr_path (so the dashboard "abandoned" metric,
// which keys on the transfer step, under-counted them).
type orderedCallLog struct {
	mu     sync.Mutex
	events []string
	steps  []ivr.Step
}

func (o *orderedCallLog) Started(context.Context, uuid.UUID, string, string, string, string, string, room.ParticipantID) room.TrackSink {
	return nil
}
func (o *orderedCallLog) Answered(string, room.ParticipantID, string) room.TrackSink { return nil }
func (o *orderedCallLog) Journey(_ string, steps []ivr.Step) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = steps
	o.events = append(o.events, "journey")
}
func (o *orderedCallLog) Ended(string, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, "ended")
}
func (o *orderedCallLog) snap() ([]string, []ivr.Step) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...), o.steps
}

// TestAbandonedCallStampsJourneyBeforeEnded reproduces a caller who is transferred
// to a team, queued (no agent free), and hangs up. The IVR walk is in progress at
// that moment, so the caller-left path races the async journey stamp. The fix makes
// stopIVR stamp the journey (incl. the transfer step) before Ended settles the CDR.
func TestAbandonedCallStampsJourneyBeforeEnded(t *testing.T) {
	tenant := uuid.New()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu: %v", err)
	}
	cl := &orderedCallLog{}
	sig := signaling.New(signaling.Config{
		SFU:       s,
		JWTSecret: testSecret,
		Flows:     transferFlows{},
		Teams:     fakeTeams{members: map[string][]string{"sales": {"alice"}}},
		Calls:     cl,
	})
	ts := httptest.NewServer(sig.Handler())
	t.Cleanup(ts.Close)

	// alice is online but already on another call → the transfer can't connect, so
	// the visitor is held in the queue and the IVR walk stays IN PROGRESS.
	alice := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	alice.send(map[string]any{"type": "register"})
	alice.readUntil("ready")
	busy := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	busy.send(map[string]any{"type": "join", "room": "busyroom", "name": "Alice"})
	busy.readUntil("joined") // alice is now on_call

	// Visitor enters the flow → IVR transfers to sales → queued (alice busy).
	visitor := dial(t, ts.URL, mintToken(t, tenant, "visitor"))
	visitor.send(map[string]any{"type": "join", "room": "call", "flow_id": "f1", "name": "Caller"})
	visitor.readUntil("joined")

	// Let the walk reach the transfer/queue so leaving now races Journey vs Ended.
	time.Sleep(400 * time.Millisecond)

	// Visitor abandons while queued.
	visitor.send(map[string]any{"type": "bye"})

	// Wait for both CDR hooks to fire.
	var seq []string
	var steps []ivr.Step
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if seq, steps = cl.snap(); len(seq) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(seq) < 2 {
		t.Fatalf("expected journey + ended hooks, got %v", seq)
	}
	// THE REGRESSION: journey must be stamped before the CDR settles.
	if seq[0] != "journey" {
		t.Fatalf("journey must be stamped before ended, got order %v", seq)
	}
	// And it must carry the transfer step — the abandonment signal the dashboard
	// counts (ivr_path @> '[{"node_type":"transfer"}]').
	hasTransfer := false
	for _, st := range steps {
		if st.NodeType == "transfer" {
			hasTransfer = true
		}
	}
	if !hasTransfer {
		t.Fatalf("abandoned journey missing transfer step: %+v", steps)
	}
}

// ensure the handler is a real http.Handler (compile guard).
var _ http.Handler = signaling.New(signaling.Config{}).Handler()

package signaling_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/sfu"
	"github.com/whatomate/tring/internal/media/signaling"
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

// ensure the handler is a real http.Handler (compile guard).
var _ http.Handler = signaling.New(signaling.Config{}).Handler()

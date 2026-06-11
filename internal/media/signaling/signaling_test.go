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
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	srv := signaling.New(signaling.Config{SFU: s, JWTSecret: testSecret})
	ts := httptest.NewServer(srv.Handler())
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

// ensure the handler is a real http.Handler (compile guard).
var _ http.Handler = signaling.New(signaling.Config{}).Handler()

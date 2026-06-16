package control_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whatomate/tring/internal/media/control"
)

const secret = "test-secret"

func wsURL(httpURL string) string { return strings.Replace(httpURL, "http", "ws", 1) }

// waitFor polls cond up to 2s.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

func TestEventUpAndCommandDown(t *testing.T) {
	events := make(chan control.Message, 4)
	srv := control.NewServer(secret, func(m control.Message) { events <- m }, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	commands := make(chan control.Message, 4)
	cli := control.NewClient(wsURL(ts.URL), secret, func(m control.Message) { commands <- m }, nil)
	go cli.Run(t.Context())

	waitFor(t, cli.Connected, "client connect")
	waitFor(t, srv.Connected, "server sees edge")

	// Event up.
	if err := cli.Send(control.Message{Type: control.EvCallStarted, Room: "t/call", FlowID: "dev"}); err != nil {
		t.Fatalf("send event: %v", err)
	}
	select {
	case m := <-events:
		if m.Type != control.EvCallStarted || m.Room != "t/call" || m.FlowID != "dev" {
			t.Fatalf("event mismatch: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not received by brain")
	}

	// Command down.
	if n := srv.Send(control.Message{Type: control.CmdPlay, Room: "t/call", File: "greeting.ogg"}); n != 1 {
		t.Fatalf("command reached %d edges, want 1", n)
	}
	select {
	case m := <-commands:
		if m.Type != control.CmdPlay || m.File != "greeting.ogg" {
			t.Fatalf("command mismatch: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command not received by edge")
	}
}

func TestUnauthorizedEdgeRejected(t *testing.T) {
	srv := control.NewServer(secret, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cli := control.NewClient(wsURL(ts.URL), "wrong-secret", nil, nil)
	go cli.Run(t.Context())

	// Should never connect with the wrong secret.
	time.Sleep(500 * time.Millisecond)
	if cli.Connected() {
		t.Fatal("edge connected with wrong secret")
	}
	if srv.Connected() {
		t.Fatal("server accepted unauthorized edge")
	}
}

func TestSendWhileDownErrors(t *testing.T) {
	cli := control.NewClient("ws://127.0.0.1:0/nope", secret, nil, nil)
	if err := cli.Send(control.Message{Type: control.EvDTMF}); err != control.ErrNotConnected {
		t.Fatalf("send while down = %v, want ErrNotConnected", err)
	}
}

func TestClientReconnects(t *testing.T) {
	events := make(chan control.Message, 8)
	srv := control.NewServer(secret, func(m control.Message) { events <- m }, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cli := control.NewClient(wsURL(ts.URL), secret, nil, nil)
	go cli.Run(t.Context())
	waitFor(t, cli.Connected, "initial connect")

	// Restart the brain: the edge should re-establish within a couple backoffs.
	srv.CloseEdges()
	waitFor(t, func() bool { return !cli.Connected() }, "edge notices drop")
	waitFor(t, cli.Connected, "edge reconnects")
	waitFor(t, srv.Connected, "server sees reconnected edge")
}

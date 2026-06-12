package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/orchestrator"
)

type fakeWebFlows struct{ flow *ivr.Flow }

func (f fakeWebFlows) Load(_ context.Context, _ string) (*ivr.Flow, error) { return f.flow, nil }

func waitTrue(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

func expectPlay(t *testing.T, plays <-chan string, want string) {
	t.Helper()
	select {
	case f := <-plays:
		if f != want {
			t.Fatalf("played %q, want %q", f, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected play %q, got nothing", want)
	}
}

// TestWebIVRDrivesFlowOverControl proves Phase 2b's brain side: the orchestrator
// runs ivr.Walk and drives a real flow (greeting → menu → transfer) entirely over
// the control channel to a scripted fake edge — no media, no FreeSWITCH.
func TestWebIVRDrivesFlowOverControl(t *testing.T) {
	flow := &ivr.Flow{
		EntryNode: "g",
		Nodes: []ivr.Node{
			{ID: "g", Type: ivr.NodeGreeting, Config: map[string]any{"audio_file": "welcome.ogg"}},
			{ID: "m", Type: ivr.NodeMenu, Config: map[string]any{
				"audio_file": "menu.ogg", "options": map[string]any{"1": "sales"},
			}},
			{ID: "t", Type: ivr.NodeTransfer, Config: map[string]any{"team": "sales"}},
			{ID: "h", Type: ivr.NodeHangup},
		},
		Edges: []ivr.Edge{
			{From: "g", To: "m", Condition: "default"},
			{From: "m", To: "t", Condition: "digit:1"},
			{From: "t", To: "h", Condition: "completed"},
		},
	}

	// Brain: control server + WebIVR.
	srv := control.NewServer("", nil, nil)
	web := orchestrator.NewWebIVR(srv, fakeWebFlows{flow}, nil)
	srv.SetOnEvent(web.OnEvent)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Fake edge: responds to play (done) + ring (completed), records what it saw.
	plays := make(chan string, 8)
	rings := make(chan control.Message, 2)
	var edge *control.Client
	onCmd := func(m control.Message) {
		switch m.Type {
		case control.CmdPlay:
			plays <- m.File
			_ = edge.Send(control.Message{ID: m.ID, OK: true})
		case control.CmdRing:
			rings <- m
			_ = edge.Send(control.Message{ID: m.ID, Outcome: "completed"})
		}
	}
	edge = control.NewClient(strings.Replace(ts.URL, "http", "ws", 1), "", onCmd, nil)
	go edge.Run(t.Context())
	waitTrue(t, edge.Connected, "edge connect")
	waitTrue(t, srv.Connected, "server sees edge")

	// Edge announces a web call started → brain runs the IVR.
	if err := edge.Send(control.Message{Type: control.EvCallStarted, Room: "tenant/call", FlowID: "f1"}); err != nil {
		t.Fatalf("send call.started: %v", err)
	}

	expectPlay(t, plays, "welcome.ogg") // greeting
	expectPlay(t, plays, "menu.ogg")    // menu prompt

	// Caller presses 1 — the edge forwards the DTMF up.
	if err := edge.Send(control.Message{Type: control.EvDTMF, Room: "tenant/call", Digit: "1"}); err != nil {
		t.Fatalf("send dtmf: %v", err)
	}

	// Transfer rings the team over the channel.
	select {
	case r := <-rings:
		if r.Team != "sales" {
			t.Fatalf("ring team = %q, want sales", r.Team)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transfer did not ring the edge")
	}
}

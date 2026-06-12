package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/orchestrator"
)

type fakeWebFlows struct{ flow *ivr.Flow }

func (f fakeWebFlows) Load(_ context.Context, _ string) (*ivr.Flow, error) { return f.flow, nil }

type fakeWebTeams struct{ members map[string][]string }

func (f fakeWebTeams) TeamSIPs(_ context.Context, _ uuid.UUID, team string) ([]string, error) {
	return f.members[team], nil
}

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

	// Brain: control server + WebIVR (resolves team "sales" → alice).
	srv := control.NewServer("", nil, nil)
	web := orchestrator.NewWebIVR(srv, fakeWebFlows{flow}, fakeWebTeams{members: map[string][]string{"sales": {"alice"}}}, nil)
	srv.SetOnEvent(web.OnEvent)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Fake edge: replies to play (done); on ring, simulates the agent answering by
	// reporting a participant joined (the brain owns the ring now).
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
			_ = edge.Send(control.Message{Type: control.EvParticipantJoined, Room: m.Room})
		}
	}
	edge = control.NewClient(strings.Replace(ts.URL, "http", "ws", 1), "", onCmd, nil)
	go edge.Run(t.Context())
	waitTrue(t, edge.Connected, "edge connect")
	waitTrue(t, srv.Connected, "server sees edge")

	tenant := uuid.New()
	room := tenant.String() + "/call"

	// Agent alice comes online (processed before call.started — same socket, in order).
	if err := edge.Send(control.Message{Type: control.EvAgentOnline, Tenant: tenant.String(), Sip: "alice"}); err != nil {
		t.Fatalf("send agent.online: %v", err)
	}
	// Edge announces a web call started → brain runs the IVR.
	if err := edge.Send(control.Message{Type: control.EvCallStarted, Room: room, FlowID: "f1"}); err != nil {
		t.Fatalf("send call.started: %v", err)
	}

	expectPlay(t, plays, "welcome.ogg") // greeting
	expectPlay(t, plays, "menu.ogg")    // menu prompt

	// Caller presses 1 — the edge forwards the DTMF up.
	if err := edge.Send(control.Message{Type: control.EvDTMF, Room: room, Digit: "1"}); err != nil {
		t.Fatalf("send dtmf: %v", err)
	}

	// Transfer: the BRAIN resolved team "sales" → online agent alice, and rings
	// her specifically over the channel.
	select {
	case r := <-rings:
		if r.Sip != "alice" {
			t.Fatalf("ring sip = %q, want alice (brain-resolved)", r.Sip)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transfer did not ring the resolved agent")
	}
}

// TestWebIVRAgentTransfer checks an agent-initiated transfer: the brain rings the
// new target into the room and, once they answer, drops the transferring agent.
func TestWebIVRAgentTransfer(t *testing.T) {
	srv := control.NewServer("", nil, nil)
	web := orchestrator.NewWebIVR(srv, nil, fakeWebTeams{members: map[string][]string{"sales": {"bob"}}}, nil)
	srv.SetOnEvent(web.OnEvent)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	rings := make(chan control.Message, 4)
	removes := make(chan control.Message, 4)
	var edge *control.Client
	onCmd := func(m control.Message) {
		switch m.Type {
		case control.CmdRing:
			rings <- m
			_ = edge.Send(control.Message{Type: control.EvParticipantJoined, Room: m.Room})
		case control.CmdRemove:
			removes <- m
		}
	}
	edge = control.NewClient(strings.Replace(ts.URL, "http", "ws", 1), "", onCmd, nil)
	go edge.Run(t.Context())
	waitTrue(t, edge.Connected, "edge connect")

	tenant := uuid.New()
	room := tenant.String() + "/call"
	if err := edge.Send(control.Message{Type: control.EvAgentOnline, Tenant: tenant.String(), Sip: "bob"}); err != nil {
		t.Fatalf("agent.online: %v", err)
	}
	// Agent "old-agent" transfers the call to team sales.
	if err := edge.Send(control.Message{
		Type: control.EvTransfer, Room: room, Participant: "old-agent", Team: "sales",
	}); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	select {
	case r := <-rings:
		if r.Sip != "bob" {
			t.Fatalf("transfer rang %q, want bob", r.Sip)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transfer did not ring the target")
	}
	select {
	case rm := <-removes:
		if rm.Participant != "old-agent" {
			t.Fatalf("removed %q, want old-agent", rm.Participant)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transferring agent was not dropped after answer")
	}
}

package signaling_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/media/signaling"
)

func newDrainServer(t *testing.T, cfg signaling.Config) (*signaling.Server, *httptest.Server) {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	cfg.SFU = s
	cfg.JWTSecret = testSecret
	sig := signaling.New(cfg)
	ts := httptest.NewServer(sig.Handler())
	t.Cleanup(ts.Close)
	return sig, ts
}

// drainAsync starts Drain and returns the number of calls it left behind.
func drainAsync(sig *signaling.Server, timeout time.Duration) chan int {
	left := make(chan int, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		left <- sig.Drain(ctx)
	}()
	return left
}

func TestDrainWithNoCallsReturnsAtOnce(t *testing.T) {
	sig, _ := newDrainServer(t, signaling.Config{})
	select {
	case n := <-drainAsync(sig, 5*time.Second):
		if n != 0 {
			t.Fatalf("calls left = %d, want 0", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain blocked with no calls")
	}
}

func TestDrainingRefusesNewWebCallAndFailsHealthz(t *testing.T) {
	sig, ts := newDrainServer(t, signaling.Config{})
	<-drainAsync(sig, time.Second)

	c := dial(t, ts.URL, mintToken(t, uuid.New(), "alice"))
	c.send(map[string]any{"type": "join", "room": "r1", "name": "Alice"})
	if m := c.readUntil("error"); m["reason"] != "gateway restarting" {
		t.Fatalf("error reason = %v", m["reason"])
	}

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d, want 503 while draining", resp.StatusCode)
	}
}

func TestDrainWaitsForLiveWebCall(t *testing.T) {
	sig, ts := newDrainServer(t, signaling.Config{DevTokens: true})
	tenant := uuid.New()
	c := dial(t, ts.URL, mintToken(t, tenant, "alice"))
	c.send(map[string]any{"type": "join", "room": "r1", "name": "Alice"})
	c.readUntil("joined")

	left := drainAsync(sig, 10*time.Second)
	select {
	case <-left:
		t.Fatal("Drain returned while a call was live")
	case <-time.After(time.Second):
	}

	// The live call keeps working: a reattach-free join by a second agent into
	// the occupied room is part of that call, not a new one.
	b := dial(t, ts.URL, mintToken(t, tenant, "bob"))
	b.send(map[string]any{"type": "join", "room": "r1", "name": "Bob"})
	b.readUntil("joined")

	c.send(map[string]any{"type": "bye"})
	b.send(map[string]any{"type": "bye"})
	select {
	case n := <-left:
		if n != 0 {
			t.Fatalf("calls left = %d, want 0", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Drain never saw the call end")
	}
}

func TestDrainTimesOutWithCallsLeft(t *testing.T) {
	sig, ts := newDrainServer(t, signaling.Config{})
	c := dial(t, ts.URL, mintToken(t, uuid.New(), "alice"))
	c.send(map[string]any{"type": "join", "room": "r1", "name": "Alice"})
	c.readUntil("joined")

	if n := <-drainAsync(sig, 600*time.Millisecond); n != 1 {
		t.Fatalf("calls left = %d, want 1", n)
	}
}

func TestDrainingRejectsInboundPhoneAndWaitsForLiveOne(t *testing.T) {
	// The flow queues the caller for a team with no agents: the call stays up.
	sig, _ := newDrainServer(t, signaling.Config{Flows: transferFlows{}})
	fp := &answeringPstn{fakePstn: newFakePstn(), answered: make(chan struct{}, 2)}
	sig.AttachPstn(fp, fakeDIDs{tenant: uuid.New(), flow: "f1"})

	// A phone call already up when the drain starts...
	fp.inbound(room.InboundCall{CallID: "c1", From: "+919900112233", To: "+916600112233"})
	select {
	case <-fp.answered:
	case <-time.After(2 * time.Second):
		t.Fatal("phone call never answered")
	}
	left := drainAsync(sig, 10*time.Second)
	select {
	case <-left:
		t.Fatal("Drain returned while a phone call was live")
	case <-time.After(time.Second):
	}

	// ...while a new one is turned away.
	go fp.inbound(room.InboundCall{CallID: "c2", From: "+919900112244", To: "+916600112233"})
	select {
	case id := <-fp.rejected:
		if id != "c2" {
			t.Fatalf("rejected %s, want c2", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new phone call not rejected while draining")
	}

	fp.src.Close()
	close(fp.done)
	select {
	case n := <-left:
		if n != 0 {
			t.Fatalf("calls left = %d, want 0", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Drain never saw the phone call end")
	}
}

// answeringPstn reports each Answer, so a test knows the call is up.
type answeringPstn struct {
	*fakePstn
	answered chan struct{}
}

func (a *answeringPstn) Answer(id room.CallID) (*room.Leg, error) {
	a.answered <- struct{}{}
	return a.fakePstn.Answer(id)
}

// When the drain times out, EndAll must actually end the calls still up: the
// phone leg is hung up (no billed dead air until FreeSWITCH's RTP timeout), the
// CDR settles as a system end, and the call stops counting as live.
func TestEndAllHangsUpAndSettlesLiveCalls(t *testing.T) {
	cl := &fakeCallLog{}
	sig, _ := newDrainServer(t, signaling.Config{Flows: transferFlows{}, Calls: cl})
	fp := &answeringPstn{fakePstn: newFakePstn(), answered: make(chan struct{}, 2)}
	sig.AttachPstn(fp, fakeDIDs{tenant: uuid.New(), flow: "f1"})

	fp.inbound(room.InboundCall{CallID: "c1", From: "+919900112233", To: "+916600112233"})
	select {
	case <-fp.answered:
	case <-time.After(2 * time.Second):
		t.Fatal("phone call never answered")
	}
	if n := <-drainAsync(sig, 300*time.Millisecond); n == 0 {
		t.Fatal("drain reported no live calls with a phone call up")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if left := sig.EndAll(ctx); left != 0 {
		t.Fatalf("calls left after EndAll = %d, want 0", left)
	}
	select {
	case id := <-fp.hangups:
		if id != "c1" {
			t.Fatalf("hung up %q, want c1", id)
		}
	default:
		t.Fatal("the phone leg was never hung up")
	}
	// callcdr keeps the first settlement; later Ended calls from the teardown
	// are no-ops there (the fake records them all).
	if _, _, ended := cl.snapshot(); ended == 0 {
		t.Fatal("CDR never settled")
	}
	cl.mu.Lock()
	first := cl.ended[0]
	cl.mu.Unlock()
	if !strings.HasSuffix(first, "|system") {
		t.Fatalf("CDR first settled as %q, want a system end", first)
	}
}

// A browser call still up when the drain times out is ended too: the
// participant gets a terminal `ended` (so the SDK doesn't reconnect into a
// dying gateway) and the room is gone.
func TestEndAllEndsWebCalls(t *testing.T) {
	sig, ts := newDrainServer(t, signaling.Config{})
	c := dial(t, ts.URL, mintToken(t, uuid.New(), "alice"))
	c.send(map[string]any{"type": "join", "room": "r1", "name": "Alice"})
	c.readUntil("joined")
	if n := <-drainAsync(sig, 300*time.Millisecond); n != 1 {
		t.Fatalf("calls left after drain = %d, want 1", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if left := sig.EndAll(ctx); left != 0 {
		t.Fatalf("calls left after EndAll = %d, want 0", left)
	}
	if m := c.readUntil("ended"); m["reason"] != "gateway restarting" {
		t.Fatalf("ended reason = %v, want gateway restarting", m["reason"])
	}
}

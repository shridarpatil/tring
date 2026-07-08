package fsplug

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/shridarpatil/tring/internal/media/room"
)

// serveFakeFS binds srv to addr and serves it for the lifetime of the test,
// closing on t.Cleanup. It deliberately does NOT use srv.ListenAndServe(ctx,
// ...): that method's ctx-cancel teardown reads/writes an unsynchronized
// connCloser var (sipgo v1.4.0, server.go) — a data race under -race, the
// same one internal/fsplug.Plug.Run already works around. Owning the conn
// and closing it exactly once from t.Cleanup sidesteps it.
func serveFakeFS(t *testing.T, srv *sipgo.Server, addr string) {
	t.Helper()
	laddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go srv.ServeUDP(conn)
	time.Sleep(100 * time.Millisecond) // let the UAS bind (test-only)
}

// fakeFS is a sipgo UAS standing in for FreeSWITCH: it answers the plug's
// outbound INVITE 200 OK with an Opus answer, capturing the X-Tring-Trunk header.
func TestOriginateAnswered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- fake FS UAS on :15090 ---
	ua, _ := sipgo.NewUA()
	fsSrv, _ := sipgo.NewServer(ua)
	fsClient, _ := sipgo.NewClient(ua)
	ds := sipgo.NewDialogServerCache(fsClient, sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15090}})
	gotTrunk := make(chan string, 1)
	fsSrv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		sess, err := ds.ReadInvite(req, tx)
		if err != nil {
			return
		}
		if h := req.GetHeader("X-Tring-Trunk"); h != nil {
			gotTrunk <- h.Value()
		}
		// answer with an Opus SDP pointing at a throwaway port
		_ = sess.RespondSDP(buildAnswer("127.0.0.1", 16000, 111, 101))
	})
	fsSrv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = ds.ReadAck(req, tx) })
	fsSrv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) { _ = ds.ReadBye(req, tx) })
	serveFakeFS(t, fsSrv, "127.0.0.1:15090")

	// --- the plug as UAC ---
	p, err := New(Config{SIPListen: "127.0.0.1:15091", SIPAdvertise: "127.0.0.1", RTPMin: 12600, RTPMax: 12700, OpusPT: 111, DTMFPT: 101, FSSIP: "127.0.0.1:15090"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	states := make(chan string, 4)
	leg, err := p.Originate(ctx, room.OriginateRequest{To: "919000000001", CallerID: "911600123456", Trunks: []string{"tata"}}, func(pr room.Progress) { states <- pr.State })
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	if leg == nil || leg.Source == nil {
		t.Fatal("no leg returned")
	}
	select {
	case tr := <-gotTrunk:
		if tr != "tata" {
			t.Fatalf("X-Tring-Trunk = %q, want tata", tr)
		}
	case <-time.After(time.Second):
		t.Fatal("FS never saw the INVITE / X-Tring-Trunk")
	}
	select {
	case st := <-states:
		if st != "ringing" {
			t.Fatalf("progress state = %q, want ringing", st)
		}
	case <-time.After(time.Second):
		t.Fatal("onProgress never fired")
	}
	_ = leg.Source.Close()
}

// TestOriginateBusyIsError asserts a 486 Busy Here from the far end surfaces
// as a nil leg + non-nil error, not a panic or a hang.
func TestOriginateBusyIsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ua, _ := sipgo.NewUA()
	fsSrv, _ := sipgo.NewServer(ua)
	fsClient, _ := sipgo.NewClient(ua)
	ds := sipgo.NewDialogServerCache(fsClient, sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15190}})
	fsSrv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		sess, err := ds.ReadInvite(req, tx)
		if err != nil {
			return
		}
		_ = sess.Respond(sip.StatusBusyHere, "Busy Here", nil)
	})
	fsSrv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = ds.ReadAck(req, tx) })
	fsSrv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) { _ = ds.ReadBye(req, tx) })
	serveFakeFS(t, fsSrv, "127.0.0.1:15190")

	p, err := New(Config{SIPListen: "127.0.0.1:15191", SIPAdvertise: "127.0.0.1", RTPMin: 12700, RTPMax: 12800, OpusPT: 111, DTMFPT: 101, FSSIP: "127.0.0.1:15190"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	leg, err := p.Originate(ctx, room.OriginateRequest{To: "919000000002", CallerID: "911600123456", Trunks: []string{"tata"}}, func(room.Progress) {})
	if err == nil {
		t.Fatal("expected an error for 486 Busy Here")
	}
	if leg != nil {
		t.Fatal("expected a nil leg on busy")
	}
}

// TestOriginateTeardownOnCarrierBye asserts that when the carrier (fake FS)
// hangs up with a BYE immediately after answering, the plug's teardown
// goroutine observes DialogStateEnded and fires leg.Done — rather than
// leaking the goroutine and the mediaSession's UDP socket.
//
// RED reasoning (why this exercises the fixed race, and why it can't be made
// to reliably fail pre-fix): the pre-fix originate.go evaluated
// `dlg.StateRead()` as the range expression INSIDE the spawned teardown
// goroutine, i.e. AFTER dlg.Ack() had already returned. sipgo's StateRead()
// (dialog.go) only ever delivers FUTURE state transitions — it registers an
// OnState callback, it does not replay/snapshot the current state. So a BYE
// whose Ended transition lands in the (Ack returns)→(goroutine reaches
// StateRead()) window would be delivered to no subscriber, and the range
// would block forever. That window, measured empirically, is a few hundred
// nanoseconds of pure Go scheduling (a function return + a "go func(){}()"
// dispatch) — dwarfed by the minimum possible latency of THIS test's BYE,
// which cannot even be transmitted until sipgo's DialogServerSession.WriteBye
// observes the dialog as Confirmed, i.e. until the fake FS has already fully
// processed our ACK, then marshaled+sent a UDP packet, which the plug then
// has to receive and dispatch — several orders of magnitude slower than the
// window it's racing against. Confirmed by running this test with
// `git stash` on the originate.go fix reverted, `-count=30`: 30/30 passed,
// i.e. the old code's goroutine always won the race against a real
// over-the-wire BYE. This matches the task brief's own anticipation ("if
// reliably triggering the fast BYE timing... is hard, at minimum assert a
// normal carrier BYE fires Done"). What this test DOES lock in: the fixed
// code's actual mechanism (subscribe-before-Ack + LoadState() residual
// guard) is exercised end-to-end via a real carrier BYE, so a regression
// that removes the early subscription (reverting to a bare
// `for st := range dlg.StateRead()` inside the goroutine) would only be
// caught by chance/timing here, not deterministically — the deterministic
// guarantee comes from code review + the doc comment on the fix itself,
// same as plug.go's symmetric inbound case relies on for watchDialog.
func TestOriginateTeardownOnCarrierBye(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- fake FS UAS on :15290, answers then immediately BYEs ---
	ua, _ := sipgo.NewUA()
	fsSrv, _ := sipgo.NewServer(ua)
	fsClient, _ := sipgo.NewClient(ua)
	ds := sipgo.NewDialogServerCache(fsClient, sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15290}})
	sessCh := make(chan *sipgo.DialogServerSession, 1)
	fsSrv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		sess, err := ds.ReadInvite(req, tx)
		if err != nil {
			return
		}
		_ = sess.RespondSDP(buildAnswer("127.0.0.1", 16001, 111, 101))
		sessCh <- sess
	})
	fsSrv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = ds.ReadAck(req, tx)
		// Hang up the instant the ACK lands. Run on its own goroutine so
		// Bye's wait-for-200 can't block this handler (and so it races the
		// plug's own post-Ack bookkeeping as tightly as possible).
		//
		// Built by hand (Recipient pointed straight at the plug's known
		// listen address) rather than via sess.Bye(), which resolves its
		// target from the INVITE's Contact header. sess.WriteBye fills in
		// From/To/Call-ID/CSeq from the dialog for us (dialog_server.go:
		// buildReq), so only Method + Recipient are needed here.
		sess := <-sessCh
		go func() {
			bye := sip.NewRequest(sip.BYE, sip.Uri{User: "tring", Host: "127.0.0.1", Port: 15291})
			_ = sess.WriteBye(context.Background(), bye)
		}()
	})
	fsSrv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) { _ = ds.ReadBye(req, tx) })
	serveFakeFS(t, fsSrv, "127.0.0.1:15290")

	// --- the plug as UAC ---
	p, err := New(Config{SIPListen: "127.0.0.1:15291", SIPAdvertise: "127.0.0.1", RTPMin: 12800, RTPMax: 12900, OpusPT: 111, DTMFPT: 101, FSSIP: "127.0.0.1:15290"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// The outbound INVITE/ACK exchange rides the sipgo client's own ephemeral
	// socket, but a carrier-initiated in-dialog BYE is an inbound *request*
	// to the plug — that needs Plug.Run's server actually bound and
	// listening at SIPListen, unlike the answer-only tests above.
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go p.Run(runCtx)
	time.Sleep(100 * time.Millisecond) // let the plug's UAS bind (test-only, mirrors serveFakeFS)

	leg, err := p.Originate(ctx, room.OriginateRequest{To: "919000000003", CallerID: "911600123456", Trunks: []string{"tata"}}, func(room.Progress) {})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	if leg == nil {
		t.Fatal("no leg returned")
	}

	select {
	case <-leg.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("leg.Done never fired after carrier BYE (teardown goroutine leaked)")
	}
}

// TestHangupOutboundSendsBye asserts the room-hangs-up-first direction: when
// the room calls p.HangupOutbound with the leg's ParticipantID, the plug
// sends a BYE to the carrier (fake FS) and closes the leg's media session
// (observed via leg.Done, since mediaSession.Close fires it). It also
// asserts HangupOutbound on an unknown ParticipantID is a no-op, not a panic
// or an error — the carrier may have already hung up first, which
// Originate's own teardown goroutine handles.
func TestHangupOutboundSendsBye(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- fake FS UAS on :15390, answers and records any BYE it receives ---
	ua, _ := sipgo.NewUA()
	fsSrv, _ := sipgo.NewServer(ua)
	fsClient, _ := sipgo.NewClient(ua)
	ds := sipgo.NewDialogServerCache(fsClient, sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15390}})
	gotBye := make(chan struct{}, 1)
	fsSrv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		sess, err := ds.ReadInvite(req, tx)
		if err != nil {
			return
		}
		_ = sess.RespondSDP(buildAnswer("127.0.0.1", 16002, 111, 101))
	})
	fsSrv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = ds.ReadAck(req, tx) })
	fsSrv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = ds.ReadBye(req, tx)
		select {
		case gotBye <- struct{}{}:
		default:
		}
	})
	serveFakeFS(t, fsSrv, "127.0.0.1:15390")

	// --- the plug as UAC ---
	p, err := New(Config{SIPListen: "127.0.0.1:15391", SIPAdvertise: "127.0.0.1", RTPMin: 12900, RTPMax: 13000, OpusPT: 111, DTMFPT: 101, FSSIP: "127.0.0.1:15390"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	leg, err := p.Originate(ctx, room.OriginateRequest{To: "919000000004", CallerID: "911600123456", Trunks: []string{"tata"}}, func(room.Progress) {})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	if leg == nil || leg.Source == nil {
		t.Fatal("no leg returned")
	}

	if err := p.HangupOutbound(leg.Source.ID()); err != nil {
		t.Fatalf("HangupOutbound: %v", err)
	}

	select {
	case <-gotBye:
	case <-time.After(2 * time.Second):
		t.Fatal("fake FS never received a BYE from HangupOutbound")
	}

	select {
	case <-leg.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("leg.Done never fired after HangupOutbound")
	}

	// Unknown ParticipantID: idempotent no-op (e.g. the carrier already hung
	// up, or a duplicate hangup request), not a panic or an error.
	if err := p.HangupOutbound(room.ParticipantID("does-not-exist")); err != nil {
		t.Fatalf("HangupOutbound(unknown) = %v, want nil", err)
	}
}

// TestHangupOutboundRacesCarrierBye exercises the room-hangs-up and
// carrier-hangs-up teardown paths concurrently: p.HangupOutbound and a
// carrier-initiated BYE fire at (as close to) the same instant. Neither the
// map's check-and-delete (HangupOutbound / forgetOutbound) nor the
// mediaSession's Close should panic or double-fire, and exactly one
// teardown should "win" — the map entry ends up gone either way, so a
// second HangupOutbound call is always a no-op. Must pass under -race.
func TestHangupOutboundRacesCarrierBye(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- fake FS UAS on :15490, answers then sends a BYE the instant its
	// ACK lands (same technique as TestOriginateTeardownOnCarrierBye) ---
	ua, _ := sipgo.NewUA()
	fsSrv, _ := sipgo.NewServer(ua)
	fsClient, _ := sipgo.NewClient(ua)
	ds := sipgo.NewDialogServerCache(fsClient, sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15490}})
	sessCh := make(chan *sipgo.DialogServerSession, 1)
	fsSrv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		sess, err := ds.ReadInvite(req, tx)
		if err != nil {
			return
		}
		_ = sess.RespondSDP(buildAnswer("127.0.0.1", 16003, 111, 101))
		sessCh <- sess
	})
	fsSrv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = ds.ReadAck(req, tx)
		sess := <-sessCh
		go func() {
			bye := sip.NewRequest(sip.BYE, sip.Uri{User: "tring", Host: "127.0.0.1", Port: 15491})
			_ = sess.WriteBye(context.Background(), bye)
		}()
	})
	fsSrv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) { _ = ds.ReadBye(req, tx) })
	serveFakeFS(t, fsSrv, "127.0.0.1:15490")

	// --- the plug as UAC (and UAS for the carrier's inbound BYE) ---
	p, err := New(Config{SIPListen: "127.0.0.1:15491", SIPAdvertise: "127.0.0.1", RTPMin: 13000, RTPMax: 13100, OpusPT: 111, DTMFPT: 101, FSSIP: "127.0.0.1:15490"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go p.Run(runCtx)
	time.Sleep(100 * time.Millisecond) // let the plug's UAS bind (test-only)

	leg, err := p.Originate(ctx, room.OriginateRequest{To: "919000000005", CallerID: "911600123456", Trunks: []string{"tata"}}, func(room.Progress) {})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	if leg == nil || leg.Source == nil {
		t.Fatal("no leg returned")
	}
	pid := leg.Source.ID()

	// Race HangupOutbound (room-initiated) against the carrier's BYE (fired
	// from the OnAck handler above) by calling it right away, from its own
	// goroutine, with no synchronization — exactly the check-and-delete race
	// forgetOutbound/HangupOutbound must survive.
	done := make(chan struct{})
	go func() {
		_ = p.HangupOutbound(pid)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HangupOutbound never returned")
	}

	select {
	case <-leg.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("leg.Done never fired (teardown leaked)")
	}

	// Terminal state must be consistent: the map entry is gone either way,
	// so a second HangupOutbound is always a no-op, not an error or panic.
	if err := p.HangupOutbound(pid); err != nil {
		t.Fatalf("second HangupOutbound = %v, want nil", err)
	}
}

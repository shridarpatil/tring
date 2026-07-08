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

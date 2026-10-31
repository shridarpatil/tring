package fsplug

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/shridarpatil/tring/internal/media/room"
)

func TestPlugInboundAnswerAndBye(t *testing.T) {
	cfg := Config{SIPListen: "127.0.0.1:15060", SIPAdvertise: "127.0.0.1", RTPMin: 12200, RTPMax: 12300, OpusPT: 111, DTMFPT: 101}
	p, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	inbound := make(chan room.InboundCall, 1)
	p.OnInbound(func(ic room.InboundCall) { inbound <- ic })

	// --- fake FreeSWITCH: a sipgo client that INVITEs the plug ---
	ua, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(ua)
	contact := sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15061}}
	dc := sipgo.NewDialogClientCache(client, contact)

	offer := []byte(fsOffer) // from sdp_test.go
	recipient := sip.Uri{User: "919000000001", Host: "127.0.0.1", Port: 15060}
	dlg, err := dc.Invite(ctx, recipient, offer, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}

	var ic room.InboundCall
	select {
	case ic = <-inbound:
	case <-time.After(2 * time.Second):
		t.Fatal("OnInbound never fired")
	}
	if ic.To != "919000000001" {
		t.Fatalf("To = %q, want the dialed DID", ic.To)
	}

	leg, err := p.Answer(ic.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dlg.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dlg.Ack(ctx); err != nil {
		t.Fatal(err)
	}

	// Client BYE → Leg.Done must fire.
	go dlg.Bye(ctx)
	select {
	case <-leg.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Leg.Done never fired on BYE")
	}
	_ = leg.Source.Close()
}

func TestPlugHangupUnansweredNoPanic(t *testing.T) {
	cfg := Config{SIPListen: "127.0.0.1:15080", SIPAdvertise: "127.0.0.1", RTPMin: 12600, RTPMax: 12700, OpusPT: 111, DTMFPT: 101}
	p, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	inbound := make(chan room.InboundCall, 1)
	p.OnInbound(func(ic room.InboundCall) { inbound <- ic })

	// --- fake FreeSWITCH: a sipgo client that INVITEs the plug ---
	ua, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(ua)
	contact := sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15081}}
	dc := sipgo.NewDialogClientCache(client, contact)

	offer := []byte(fsOffer) // from sdp_test.go
	recipient := sip.Uri{User: "919000000002", Host: "127.0.0.1", Port: 15080}
	_, err = dc.Invite(ctx, recipient, offer, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}

	var ic room.InboundCall
	select {
	case ic = <-inbound:
	case <-time.After(2 * time.Second):
		t.Fatal("OnInbound never fired")
	}

	// Hangup WITHOUT ever calling Answer must not panic and must return nil.
	if err := p.Hangup(ic.CallID); err != nil {
		t.Fatalf("Hangup on unanswered call returned error: %v", err)
	}

	// The client's INVITE will simply time out later (ringTimeout on the
	// plug side) since we never responded; nothing further to assert.
}

func TestPlugRejectUnknownDID(t *testing.T) {
	cfg := Config{SIPListen: "127.0.0.1:15070", SIPAdvertise: "127.0.0.1", RTPMin: 12400, RTPMax: 12500, OpusPT: 111, DTMFPT: 101}
	p, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	p.OnInbound(func(ic room.InboundCall) { _ = p.Reject(ic.CallID) })

	ua, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(ua)
	contact := sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15071}}
	dc := sipgo.NewDialogClientCache(client, contact)
	dlg, err := dc.Invite(ctx, sip.Uri{User: "999", Host: "127.0.0.1", Port: 15070}, []byte(fsOffer), sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}
	err = dlg.WaitAnswer(ctx, sipgo.AnswerOptions{})
	if err == nil {
		t.Fatal("expected a non-2xx (reject), got an answer")
	}
}

// TestPlugAnswersAtCarrierRate: the X-Tring-Audio-Rate FreeSWITCH puts on a
// carrier call caps the Opus answer at that rate; a call without it (WhatsApp)
// stays full band.
func TestPlugAnswersAtCarrierRate(t *testing.T) {
	cfg := Config{SIPListen: "127.0.0.1:15070", SIPAdvertise: "127.0.0.1", RTPMin: 12400, RTPMax: 12500, OpusPT: 111, DTMFPT: 101}
	p, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	inbound := make(chan room.InboundCall, 1)
	p.OnInbound(func(ic room.InboundCall) { inbound <- ic })

	ua, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(ua)
	dc := sipgo.NewDialogClientCache(client, sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: 15071}})

	for _, tc := range []struct {
		header string
		want   string // in the answer's Opus fmtp; "" = no cap
	}{
		{"8000", "maxplaybackrate=8000;sprop-maxcapturerate=8000"},
		{"", ""},
	} {
		hdrs := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
		if tc.header != "" {
			hdrs = append(hdrs, sip.NewHeader("X-Tring-Audio-Rate", tc.header))
		}
		dlg, err := dc.Invite(ctx, sip.Uri{User: "919000000001", Host: "127.0.0.1", Port: 15070}, []byte(fsOffer), hdrs...)
		if err != nil {
			t.Fatal(err)
		}
		var ic room.InboundCall
		select {
		case ic = <-inbound:
		case <-time.After(2 * time.Second):
			t.Fatal("OnInbound never fired")
		}
		leg, err := p.Answer(ic.CallID)
		if err != nil {
			t.Fatal(err)
		}
		if err := dlg.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
			t.Fatal(err)
		}
		ans := string(dlg.InviteResponse.Body())
		if tc.want != "" && !strings.Contains(ans, tc.want) {
			t.Errorf("X-Tring-Audio-Rate %q: answer lacks %q:\n%s", tc.header, tc.want, ans)
		}
		if tc.want == "" && strings.Contains(ans, "maxplaybackrate") {
			t.Errorf("no X-Tring-Audio-Rate: answer caps Opus:\n%s", ans)
		}
		_ = dlg.Ack(ctx)
		_ = dlg.Bye(ctx)
		<-leg.Done
	}
}

// inviteStatus INVITEs a plug listening on 127.0.0.1:port and returns the
// final response's status code (200 if it answered).
func inviteStatus(t *testing.T, ctx context.Context, port, fromPort int) int {
	t.Helper()
	ua, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(ua)
	contact := sip.ContactHeader{Address: sip.Uri{User: "fs", Host: "127.0.0.1", Port: fromPort}}
	dc := sipgo.NewDialogClientCache(client, contact)
	dlg, err := dc.Invite(ctx, sip.Uri{User: "919000000001", Host: "127.0.0.1", Port: port}, []byte(fsOffer), sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}
	err = dlg.WaitAnswer(ctx, sipgo.AnswerOptions{})
	var de *sipgo.ErrDialogResponse
	if errors.As(err, &de) {
		return de.Res.StatusCode
	}
	if err != nil {
		t.Fatal(err)
	}
	return 200
}

// TestPlugAllowedSources: the plug takes SIP from this host (where FreeSWITCH
// runs) and from AllowFrom, and nothing else.
func TestPlugAllowedSources(t *testing.T) {
	cfg := Config{SIPListen: "127.0.0.1:15590", SIPAdvertise: "127.0.0.1", RTPMin: 13200, RTPMax: 13300,
		AllowFrom: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}}
	p, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for src, want := range map[string]bool{
		"127.0.0.1:5080":          true,  // FreeSWITCH on this host
		"198.51.100.7:5080":       true,  // AllowFrom
		"203.0.113.9:5080":        false, // the internet
		"[::ffff:127.0.0.1]:5080": true,
		"not-an-addr":             false,
	} {
		if got := p.allowed(src); got != want {
			t.Errorf("allowed(%q) = %v, want %v", src, got, want)
		}
	}
}

// TestPlugRejectsDisallowedSource: an INVITE from outside the allow-list gets
// 403 and never reaches the signaling layer as an inbound call.
func TestPlugRejectsDisallowedSource(t *testing.T) {
	cfg := Config{SIPListen: "127.0.0.1:15690", SIPAdvertise: "127.0.0.1", RTPMin: 13400, RTPMax: 13500, OpusPT: 111, DTMFPT: 101}
	p, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	p.allow = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")} // loopback no longer allowed
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go p.Run(ctx)
	p.OnInbound(func(room.InboundCall) { t.Error("OnInbound fired for a disallowed source") })

	if got := inviteStatus(t, ctx, 15690, 15691); got != sip.StatusForbidden {
		t.Fatalf("status = %d, want 403", got)
	}
}

// TestPlugRejectsAtCapacity: once live calls fill the RTP port range, a new
// INVITE gets 503 instead of parking a goroutine for the ring timeout.
func TestPlugRejectsAtCapacity(t *testing.T) {
	cfg := Config{SIPListen: "127.0.0.1:15790", SIPAdvertise: "127.0.0.1", RTPMin: 13600, RTPMax: 13600, OpusPT: 111, DTMFPT: 101}
	p, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	p.calls["existing"] = &call{responded: make(chan struct{})} // the one port's call
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go p.Run(ctx)
	p.OnInbound(func(room.InboundCall) { t.Error("OnInbound fired past capacity") })

	if got := inviteStatus(t, ctx, 15790, 15791); got != sip.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
}

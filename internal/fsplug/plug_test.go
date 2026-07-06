package fsplug

import (
	"context"
	"log/slog"
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

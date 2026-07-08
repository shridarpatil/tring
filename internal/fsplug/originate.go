package fsplug

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/shridarpatil/tring/internal/media/room"
)

// buildOffer is the outbound SDP offer. Structurally identical to buildAnswer
// (Opus-only + telephone-event forces FS to transcode toward the carrier).
func buildOffer(localIP string, localPort int, opusPT, dtmfPT uint8) []byte {
	return buildAnswer(localIP, localPort, opusPT, dtmfPT)
}

// splitHostPort splits an "ip:port" address, defaulting to port 5060 if no
// port is present (or the input can't be split).
func splitHostPort(addr string) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 5060
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 5060
	}
	return host, port
}

// Originate places an outbound call via FreeSWITCH and returns the answered leg.
// It implements room.Originator. Originate blocks until the call is answered,
// fails, or ctx is cancelled; onProgress is called with a synthetic "ringing"
// event once the INVITE is sent (tracking real 180/183 is a later refinement).
func (p *Plug) Originate(ctx context.Context, req room.OriginateRequest, onProgress func(room.Progress)) (*room.Leg, error) {
	if len(req.Trunks) == 0 {
		return nil, fmt.Errorf("fsplug: originate with no trunk")
	}
	m, err := newMediaSession(room.ParticipantID("out-"+req.To), p.cfg.SIPAdvertise, p.cfg.RTPMin, p.cfg.RTPMax, p.cfg.OpusPT, p.cfg.DTMFPT)
	if err != nil {
		return nil, err
	}
	offer := buildOffer(p.cfg.SIPAdvertise, m.localPort(), p.cfg.OpusPT, p.cfg.DTMFPT)

	// INVITE FreeSWITCH: destination in the RURI, caller-ID in From, trunk in a
	// custom header the FS outbound dialplan reads (sip_h_X-Tring-Trunk).
	//
	// From MUST carry a tag param (RFC 3261 §8.1.1.3) — sipgo's dialog layer
	// rejects an INVITE whose From lacks one ("missing tag param in From
	// header"), and its own default-header builder
	// (client.go:clientRequestBuildReq) only synthesizes a tagged From when
	// no From header is present at all. Passing a bare generic header (as
	// the design draft first assumed) satisfies "From is present" without a
	// tag, so the far end rejects every INVITE — build a proper FromHeader
	// with its own tag instead.
	host, port := splitHostPort(p.cfg.FSSIP)
	recipient := sip.Uri{User: req.To, Host: host, Port: port}
	from := &sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: req.CallerID, Host: p.cfg.SIPAdvertise},
	}
	from.Params.Add("tag", sip.GenerateTagN(16))
	trunk := sip.NewHeader("X-Tring-Trunk", req.Trunks[0])
	ct := sip.NewHeader("Content-Type", "application/sdp")

	dlg, err := p.dc.Invite(ctx, recipient, offer, ct, from, trunk)
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("fsplug: invite: %w", err)
	}
	if onProgress != nil {
		onProgress(room.Progress{State: "ringing"})
	}
	if err := dlg.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		m.Close()
		return nil, fmt.Errorf("fsplug: outbound not answered: %w", err) // busy/no-answer/failed
	}
	ip, rtpPort, err := parseOffer(dlg.InviteResponse.Body()) // the 200's answer SDP
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("fsplug: bad answer sdp: %w", err)
	}
	m.setRemote(ip, rtpPort)
	if err := dlg.Ack(ctx); err != nil {
		m.Close()
		return nil, fmt.Errorf("fsplug: ack: %w", err)
	}
	go func() { // teardown when the far side (carrier) hangs up
		for st := range dlg.StateRead() {
			if st == sip.DialogStateEnded {
				break
			}
		}
		m.Close()
	}()
	return &room.Leg{
		Source: m,
		Write:  func(pkt *rtp.Packet) error { return m.write(pkt) },
		DTMF:   m.dtmf(),
		Done:   m.done(),
	}, nil
}

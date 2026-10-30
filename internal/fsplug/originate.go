package fsplug

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/shridarpatil/tring/internal/media/room"
)

// carrierRate is the outbound carrier leg's audio rate: FreeSWITCH's
// tring_outbound_trunk (dialplan/default.xml) bridges to the trunk with G.711
// only.
const carrierRate = 8000

// buildOffer is the outbound SDP offer. Structurally identical to buildAnswer
// (Opus-only + telephone-event forces FS to transcode toward the carrier), at
// the carrier's 8 kHz.
func buildOffer(localIP string, localPort int, opusPT, dtmfPT uint8) []byte {
	return buildAnswer(localIP, localPort, opusPT, dtmfPT, carrierRate)
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

// terminateEstablished best-effort tears down a dialog that already
// reached 200 OK (state Established) but failed before Originate could
// hand back a leg — the carrier believes the call is up, so returning
// without cleanup would strand a live channel. Bye requires a Confirmed
// (acked) dialog, so Ack first, but only if the dialog isn't already
// Confirmed: sipgo's WriteAck chains a NEW OnRetransmission handler on
// every call (dialog_client.go), so an unconditional second Ack after one
// that already succeeded would mean two ACKs go out if the carrier
// retransmits the 200. Deliberately uses context.Background() rather than
// the ctx Originate was called with: that ctx is plausibly the very
// reason we're in this cleanup path (near-expired/cancelled), and
// HangupOutbound's cleanup is background-context for the same reason.
// Errors are ignored throughout — this runs only on an already-failing
// path, as cleanup.
func terminateEstablished(dlg *sipgo.DialogClientSession) {
	if dlg.LoadState() != sip.DialogStateConfirmed {
		_ = dlg.Ack(context.Background())
	}
	_ = dlg.Bye(context.Background())
}

// Originate places an outbound call via FreeSWITCH and returns the answered leg.
// It implements room.Originator. Originate blocks until the call is answered,
// fails, or ctx is cancelled; onProgress is called with a synthetic "ringing"
// event once the INVITE is sent (tracking real 180/183 is a later refinement).
func (p *Plug) Originate(ctx context.Context, req room.OriginateRequest, onProgress func(room.Progress)) (*room.Leg, error) {
	if len(req.Trunks) == 0 {
		return nil, fmt.Errorf("fsplug: originate with no trunk")
	}
	m, err := newMediaSession(room.ParticipantID("out-"+uuid.NewString()), p.cfg.SIPAdvertise, p.cfg.RTPMin, p.cfg.RTPMax, p.cfg.OpusPT, p.cfg.DTMFPT)
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
	// Subscribe to the dialog state BEFORE Ack (and before touching the answer
	// SDP), mirroring plug.go's Answer/watchDialog: StateRead() only delivers
	// FUTURE transitions, it does not replay the current state, so a carrier
	// BYE landing in the WaitAnswer→Ack window would otherwise be missed
	// entirely and the teardown goroutine below would range forever on a
	// channel that already dropped its one Ended event — leaking the
	// goroutine, the mediaSession's UDP socket, and Leg.Done.
	stateCh := dlg.StateRead()
	ip, rtpPort, err := parseOffer(dlg.InviteResponse.Body()) // the 200's answer SDP
	if err != nil {
		// The 200 was already received (dialog Established) — the carrier
		// believes the call is up even though we're about to fail out.
		// Best-effort tear it down rather than stranding a live channel.
		terminateEstablished(dlg)
		m.Close()
		return nil, fmt.Errorf("fsplug: bad answer sdp: %w", err)
	}
	m.setRemote(ip, rtpPort)
	if err := dlg.Ack(ctx); err != nil {
		// Same as above: Established, so best-effort terminate before
		// surfacing the error, even though the Ack we just tried failed.
		terminateEstablished(dlg)
		m.Close()
		return nil, fmt.Errorf("fsplug: ack: %w", err)
	}

	// Register the dialog (keyed by the leg's ParticipantID) so
	// HangupOutbound can find it when the room hangs up first. Must happen
	// after Ack succeeds — this map only ever holds calls Originate is
	// about to hand back as a live leg. pid is call-unique (out-<uuid>), not
	// out-<To>: two concurrent Originate calls to the SAME number must not
	// share a map key, or one call's teardown could delete the OTHER's
	// still-live entry and strand it un-hangup-able.
	pid := m.ID()
	oc := &outboundCall{dlg: dlg, m: m}
	p.mu.Lock()
	p.outbound[pid] = oc
	p.mu.Unlock()

	go func() { // teardown when the far side (carrier) hangs up
		for st := range stateCh {
			if st == sip.DialogStateEnded {
				break
			}
		}
		p.forgetOutbound(pid, oc)
		m.Close()
	}()
	// Residual guard: StateRead()'s callback only fires for transitions that
	// happen AFTER it's registered, so a BYE fully processed by the network
	// reader goroutine before our call above reached the OnState
	// registration (an even earlier race than the WaitAnswer→Ack window)
	// would still never be seen by the goroutine's channel range. Checking
	// LoadState() closes that gap; m.Close() is idempotent (rtp.go) so a
	// double-close racing the goroutine above is safe, and forgetOutbound
	// is a no-op if the teardown goroutine (or a racing HangupOutbound)
	// already removed the entry.
	if dlg.LoadState() == sip.DialogStateEnded {
		p.forgetOutbound(pid, oc)
		m.Close()
	}
	return &room.Leg{
		Source: m,
		Write:  func(pkt *rtp.Packet) error { return m.write(pkt) },
		DTMF:   m.dtmf(),
		Done:   m.done(),
	}, nil
}

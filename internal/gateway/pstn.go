package gateway

// pstn.go — the FreeSWITCH adapter behind room.PstnGateway (Phase 4: FS as a
// PLUG, docs/ARCHITECTURE.md).
//
// FreeSWITCH's only remaining job on this path is the carrier edge: terminate
// the trunk (SIP/G.711 toward the telco), transcode to Opus, and hand the leg to
// the media plane. The dialplan routes an inbound DID to the gateway's SIP UAS
// (sofia/gateway/tring-gw) with the DID in X-Tring-DID; this adapter answers the
// INVITE and exposes the leg as a room.PstnLeg — caller audio in (Opus RTP),
// room audio out, RFC2833 keypresses as digits. The media plane never sees SIP
// or FS. Swapping FS for another stack (a sipgo trunk, a WebRTC trunk provider)
// = implementing room.PstnGateway in a sibling package; nothing else changes.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
	"github.com/pion/rtp"

	"github.com/whatomate/tring/internal/media/room"
)

// didHeader marks an FS→gateway INVITE as an inbound PHONE call for the plug
// (vs a legacy agent leg) and carries the dialed DID.
const didHeader = "X-Tring-DID"

// answerTimeout bounds how long the consumer may sit on OnInbound before the
// adapter 408s the call (protects the carrier leg from a hung consumer).
const answerTimeout = 25 * time.Second

// errUnknownCall is returned for Answer/Reject/Hangup on an unknown CallID.
var errUnknownCall = errors.New("pstn: unknown call id")

// PstnAdapter implements room.PstnGateway over the gateway's existing FS SIP
// machinery (fsClient). One instance per gateway; built when FS is configured.
type PstnAdapter struct {
	g *Gateway

	mu      sync.Mutex
	calls   map[room.CallID]*pstnCall
	inbound func(room.InboundCall)
}

type pstnCall struct {
	dlg    *sipgo.DialogServerSession
	fsAddr *net.UDPAddr
	opusPT uint8
	dtmfPT uint8

	// proceed is closed once a FINAL SIP response went out (Answer's 200 or
	// Reject's 486). handleInvite blocks on it: sipgo terminates the INVITE
	// transaction when the OnInvite handler returns, so the handler must stay
	// alive until the final response is sent (same reason the agent-leg handler
	// blocks). proceedOnce guards the close.
	proceed     chan struct{}
	proceedOnce sync.Once

	// set on Answer
	rtp  *net.UDPConn
	src  *pstnSource
	dtmf chan string
	done chan struct{}
	once sync.Once
}

// finalSent marks that a final SIP response went out, releasing handleInvite.
func (c *pstnCall) finalSent() { c.proceedOnce.Do(func() { close(c.proceed) }) }

func newPstnAdapter(g *Gateway) *PstnAdapter {
	return &PstnAdapter{g: g, calls: map[room.CallID]*pstnCall{}}
}

// Pstn returns the gateway's phone plug (nil when FS isn't configured).
func (g *Gateway) Pstn() room.PstnGateway {
	if g.pstn == nil {
		return nil // typed-nil guard: callers compare against plain nil
	}
	return g.pstn
}

// OnInbound implements room.PstnGateway.
func (a *PstnAdapter) OnInbound(f func(room.InboundCall)) {
	a.mu.Lock()
	a.inbound = f
	a.mu.Unlock()
}

// handleInvite is called from the gateway's SIP UAS for INVITEs carrying
// X-Tring-DID: it registers the call and raises OnInbound; the consumer then
// Answers (media starts) or Rejects.
func (a *PstnAdapter) handleInvite(req *sip.Request, tx sip.ServerTransaction, did string) {
	from := callerFrom(req)
	fsAddr, opusPT, dtmfPT, err := parseSDPMedia(string(req.Body()))
	if err != nil {
		a.g.log.Error("pstn: bad sdp from fs", "err", err)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptableHere, "Bad SDP", nil))
		return
	}
	dlg, err := a.g.fs.ds.ReadInvite(req, tx)
	if err != nil {
		a.g.log.Error("pstn: read invite failed", "err", err)
		return
	}
	if err := dlg.Respond(sip.StatusRinging, "Ringing", nil); err != nil {
		a.g.log.Error("pstn: 180 failed", "err", err)
	}

	id := room.CallID(uuid.NewString())
	call := &pstnCall{dlg: dlg, fsAddr: fsAddr, opusPT: opusPT, dtmfPT: dtmfPT, proceed: make(chan struct{})}
	a.mu.Lock()
	a.calls[id] = call
	handler := a.inbound
	a.mu.Unlock()

	a.g.log.Info("pstn: inbound call", "did", did, "from", from, "call", id)
	if handler == nil {
		a.g.log.Warn("pstn: no inbound consumer registered, rejecting", "did", did)
		_ = dlg.Respond(sip.StatusServiceUnavailable, "No Consumer", nil)
		a.drop(id)
		return
	}

	// Caller hangs up mid-ring → CANCEL (sipgo auto-487s; we just clean up).
	cancelled := make(chan struct{})
	var cancelOnce sync.Once
	tx.OnCancel(func(*sip.Request) { cancelOnce.Do(func() { close(cancelled) }) })

	handler(room.InboundCall{CallID: id, From: from, To: did})

	// Block until a FINAL response went out (Answer/Reject from the consumer),
	// the caller cancelled, or the consumer never decided (→ 408). Returning
	// earlier would let sipgo terminate the INVITE transaction under us.
	select {
	case <-call.proceed:
	case <-cancelled:
		a.g.log.Info("pstn: caller hung up mid-ring", "call", id)
		a.drop(id)
	case <-time.After(answerTimeout):
		a.g.log.Warn("pstn: call not answered by consumer, 408", "call", id)
		_ = dlg.Respond(sip.StatusRequestTimeout, "No Answer", nil)
		a.drop(id)
	}
}

// Answer implements room.PstnGateway: 200-OK the leg and start media. The
// returned PstnLeg plugs straight into sfu.AttachPort.
func (a *PstnAdapter) Answer(id room.CallID) (*room.PstnLeg, error) {
	a.mu.Lock()
	call := a.calls[id]
	a.mu.Unlock()
	if call == nil {
		return nil, errUnknownCall
	}

	rtpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(a.g.fs.cfg.AdvertIP), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("pstn: rtp socket: %w", err)
	}
	localPort := rtpConn.LocalAddr().(*net.UDPAddr).Port
	if err := call.dlg.RespondSDP([]byte(buildSDP(a.g.fs.cfg.AdvertIP, localPort, call.opusPT, call.dtmfPT))); err != nil {
		rtpConn.Close()
		call.finalSent() // release handleInvite either way
		return nil, fmt.Errorf("pstn: 200 ok: %w", err)
	}
	call.finalSent()

	call.rtp = rtpConn
	call.src = &pstnSource{id: room.ParticipantID("pstn-" + string(id)), pkts: make(chan *rtp.Packet, 64)}
	call.dtmf = make(chan string, 8)
	call.done = make(chan struct{})

	go a.readLoop(id, call)
	// FS (or the carrier) hanging up ends the dialog → tear the leg down.
	go func() {
		for state := range call.dlg.StateRead() {
			if state == sip.DialogStateEnded {
				a.endCall(id, call)
				return
			}
		}
	}()

	a.g.log.Info("pstn: answered", "call", id, "fs_rtp", call.fsAddr)
	return &room.PstnLeg{
		Source: call.src,
		Write:  a.writerFor(call),
		DTMF:   call.dtmf,
		Done:   call.done,
	}, nil
}

// Reject implements room.PstnGateway (pre-answer decline).
func (a *PstnAdapter) Reject(id room.CallID) error {
	a.mu.Lock()
	call := a.calls[id]
	a.mu.Unlock()
	if call == nil {
		return errUnknownCall
	}
	_ = call.dlg.Respond(sip.StatusBusyHere, "Rejected", nil)
	call.finalSent()
	a.drop(id)
	return nil
}

// Hangup implements room.PstnGateway: end an answered leg (BYE toward FS).
func (a *PstnAdapter) Hangup(id room.CallID) error {
	a.mu.Lock()
	call := a.calls[id]
	a.mu.Unlock()
	if call == nil {
		return errUnknownCall
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = call.dlg.Bye(ctx)
	a.endCall(id, call)
	return nil
}

// readLoop pumps FS→room: Opus packets feed the ParticipantSource; RFC2833
// telephone-event packets become digits on the DTMF channel.
func (a *PstnAdapter) readLoop(id room.CallID, call *pstnCall) {
	buf := make([]byte, 1500)
	var lastDTMFTs uint32
	var lastDTMFFired bool
	for {
		n, _, err := call.rtp.ReadFromUDP(buf)
		if err != nil {
			a.endCall(id, call)
			return
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(append([]byte(nil), buf[:n]...)); err != nil {
			continue
		}
		switch uint8(pkt.PayloadType) {
		case call.dtmfPT:
			// RFC 2833: payload = event(1) | E-bit+volume(1) | duration(2). Fire
			// once per event on the END bit; end packets are retransmitted 3x with
			// the same RTP timestamp, so dedupe by it.
			if len(pkt.Payload) >= 2 && pkt.Payload[1]&0x80 != 0 {
				if !lastDTMFFired || pkt.Timestamp != lastDTMFTs {
					lastDTMFTs, lastDTMFFired = pkt.Timestamp, true
					if d := dtmfDigit(pkt.Payload[0]); d != "" {
						select {
						case call.dtmf <- d:
						default:
						}
					}
				}
			} else {
				lastDTMFFired = false
			}
		case call.opusPT:
			call.src.push(pkt)
		}
	}
}

// writerFor returns the room→FS writer: re-PT to FS's negotiated Opus PT and
// send to its RTP endpoint. (seq/ts/SSRC were already re-stamped by the port.)
func (a *PstnAdapter) writerFor(call *pstnCall) func(*rtp.Packet) error {
	return func(pkt *rtp.Packet) error {
		pkt.Header.PayloadType = call.opusPT
		b, err := pkt.Marshal()
		if err != nil {
			return err
		}
		_, err = call.rtp.WriteToUDP(b, call.fsAddr)
		return err
	}
}

// endCall tears an answered leg down (idempotent): media socket, source, done.
func (a *PstnAdapter) endCall(id room.CallID, call *pstnCall) {
	call.once.Do(func() {
		if call.rtp != nil {
			_ = call.rtp.Close()
		}
		if call.src != nil {
			_ = call.src.Close()
		}
		if call.done != nil {
			close(call.done)
		}
		a.g.log.Info("pstn: call ended", "call", id)
	})
	a.drop(id)
}

// drop forgets a call (pre- or post-answer).
func (a *PstnAdapter) drop(id room.CallID) {
	a.mu.Lock()
	delete(a.calls, id)
	a.mu.Unlock()
}

func dtmfDigit(event byte) string {
	switch {
	case event <= 9:
		return string(rune('0' + event))
	case event == 10:
		return "*"
	case event == 11:
		return "#"
	}
	return ""
}

// pstnSource is the caller's audio as a room.ParticipantSource (Opus packets
// from the FS leg; PT/SSRC are rewritten per-subscriber by the fan-out track).
type pstnSource struct {
	id   room.ParticipantID
	pkts chan *rtp.Packet

	mu     sync.Mutex
	closed bool
}

func (s *pstnSource) ID() room.ParticipantID { return s.id }

// push delivers a packet unless the source is closed (guards the close/send
// race with readLoop) — dropping rather than stalling when the buffer is full.
func (s *pstnSource) push(pkt *rtp.Packet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.pkts <- pkt:
	default:
	}
}

func (s *pstnSource) ReadRTP() (*rtp.Packet, error) {
	pkt, ok := <-s.pkts
	if !ok {
		return nil, errors.New("pstn: leg closed")
	}
	return pkt, nil
}

func (s *pstnSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.pkts)
	}
	return nil
}

var _ room.PstnGateway = (*PstnAdapter)(nil)

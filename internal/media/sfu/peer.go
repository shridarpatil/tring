package sfu

import (
	"log/slog"
	"sync"

	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/room"
)

// Signaler is how a Peer sends SDP/ICE to its client. The /rtc WS layer (M2b)
// implements it; tests fake it. The SFU is the SOLE offerer — it only ever
// sends offers (never answers), so there's no offer/answer glare to resolve.
// Methods may be called from pion callback goroutines; implementations must be
// safe for concurrent use.
type Signaler interface {
	SendOffer(sdp string) error
	SendCandidate(c webrtc.ICECandidateInit) error
}

// Peer is one participant's PeerConnection plus its publish/subscribe state.
//
// Negotiation model — server as sole offerer (the proven pion SFU pattern):
// the PC is created with a recvonly audio transceiver so the server's offer
// invites the client's mic, and EVERY (re)negotiation is a server offer that
// the client answers (HandleAnswer). The client never offers, so there is no
// glare. Subscriptions are added as dedicated sendonly transceivers, leaving
// the recvonly publish transceiver untouched. Identity is the stable
// room.ParticipantID, so the fan-out survives a transport swap (M4).
type Peer struct {
	pid    room.ParticipantID
	roomID room.RoomID
	pc     *webrtc.PeerConnection
	sig    Signaler
	sfu    *SFU
	log    *slog.Logger

	// OnConnected / OnClosed let the signaling layer mirror transport state onto
	// the room.Participant. nil in standalone tests.
	OnConnected func()
	OnClosed    func()

	mu          sync.Mutex
	haveRemote  bool                                       // an answer has been applied → candidates can be added
	pending     []webrtc.ICECandidateInit                  // candidates that arrived before the first answer
	subs        map[room.ParticipantID][]*webrtc.RTPSender // senders delivering each source's tracks to me
	started     bool                                       // the initial offer has been sent
	dirty       bool                                       // a subscription was removed (Leave) → needs a renegotiation
	negotiating bool                                       // an offer is outstanding (awaiting the client's answer)
	needSync    bool                                       // tracks changed while negotiating → renegotiate after the answer
	closed      bool
}

// markReplaced marks this peer superseded by a reconnect: it won't be torn down
// as the current slot, and its OnClosed is cleared so closing its (old) PC
// doesn't drop the participant. The publication it fed lives on for the new PC.
func (p *Peer) markReplaced() {
	p.mu.Lock()
	p.closed = true
	p.OnClosed = nil
	p.mu.Unlock()
}

// ID returns the peer's participant id.
func (p *Peer) ID() room.ParticipantID { return p.pid }

// HandleAnswer applies the client's answer to a server offer, then renegotiates
// if a subscription changed while the offer was outstanding.
func (p *Peer) HandleAnswer(sdp string) error {
	p.mu.Lock()
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		p.mu.Unlock()
		return err
	}
	p.haveRemote = true
	p.flushPendingLocked()
	p.negotiating = false
	resync := p.needSync
	p.needSync = false
	p.mu.Unlock()
	if resync {
		p.Negotiate()
	}
	return nil
}

// HandleCandidate adds a trickled ICE candidate, queueing it until the first
// answer is applied (AddICECandidate errors before a remote description exists).
func (p *Peer) HandleCandidate(c webrtc.ICECandidateInit) error {
	p.mu.Lock()
	if !p.haveRemote {
		p.pending = append(p.pending, c)
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	return p.pc.AddICECandidate(c)
}

func (p *Peer) flushPendingLocked() {
	for _, c := range p.pending {
		if err := p.pc.AddICECandidate(c); err != nil {
			p.log.Warn("add pending candidate", "err", err)
		}
	}
	p.pending = nil
}

// Negotiate brings this peer's subscriptions in sync with the room (adds a
// sendonly transceiver for every other peer's fan-out track it isn't sending
// yet) and, if anything is pending, sends a fresh server offer. Serialized per
// peer: while an offer is outstanding or the PC is non-stable it defers and
// re-runs after the answer (HandleAnswer). The first call (at join) sends the
// initial offer even with no subscriptions, because the recvonly publish
// transceiver added at Join needs an offer to invite the client's mic.
func (p *Peer) Negotiate() {
	// Snapshot the other participants' fan-out tracks WITHOUT holding p.mu (it
	// locks the SFU, not peer locks, so no peer↔peer lock cycle).
	sources := p.sfu.publicationsExcept(p.roomID, p.pid)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.negotiating || p.pc.SignalingState() != webrtc.SignalingStateStable {
		p.needSync = true
		p.mu.Unlock()
		return
	}
	p.needSync = false

	// Subscribe to any fan-out tracks we're not sending yet. Dedicated sendonly
	// transceivers — never AddTrack, which would reuse the recvonly publish
	// transceiver and cannibalize it.
	first := !p.started
	added := false
	for _, src := range sources {
		if _, ok := p.subs[src.pid]; ok {
			continue
		}
		for _, track := range src.tracks {
			tr, err := p.pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionSendonly,
			})
			if err != nil {
				p.log.Warn("subscribe add transceiver", "from", src.pid, "err", err)
				continue
			}
			p.subs[src.pid] = append(p.subs[src.pid], tr.Sender())
			drainSender(tr.Sender())
			added = true
		}
	}
	// The first call always offers (the recvonly publish transceiver needs an
	// offer to invite the client's mic); later calls only offer when something
	// actually changed (a subscription added here, or removed via Leave), to
	// avoid churning the PC.
	if !first && !added && !p.dirty {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.dirty = false

	p.negotiating = true
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		p.negotiating = false
		p.mu.Unlock()
		p.log.Error("create offer", "err", err)
		return
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		p.negotiating = false
		p.mu.Unlock()
		p.log.Error("set local offer", "err", err)
		return
	}
	p.mu.Unlock()
	if err := p.sig.SendOffer(offer.SDP); err != nil {
		p.log.Warn("send offer", "err", err)
	}
}

// drainSender reads (and discards) a sender's RTCP so its receive buffer doesn't
// fill — required for every subscription sender. Ends when the sender closes.
func drainSender(s *webrtc.RTPSender) {
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := s.Read(buf); err != nil {
				return
			}
		}
	}()
}

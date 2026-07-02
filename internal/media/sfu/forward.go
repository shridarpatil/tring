package sfu

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
)

// sourcePub is a snapshot of one other participant's publication, used to wire
// subscriptions during Negotiate.
type sourcePub struct {
	pid    room.ParticipantID
	tracks []*webrtc.TrackLocalStaticRTP
}

// Join creates a PeerConnection for a participant and registers it. If the
// participant already has a peer (a reattach after a refresh), the old PC is
// closed but its publication and subscribers are KEPT — the new PC re-points
// into the same fan-out track, so the other peers never renegotiate. The server
// is the sole offerer: after wiring its client-side handlers, the caller calls
// Peer.Negotiate to send the initial offer, then drives HandleAnswer /
// HandleCandidate. Set Peer.OnConnected / OnClosed to mirror transport state.
func (s *SFU) Join(roomID room.RoomID, pid room.ParticipantID, sig Signaler) (*Peer, error) {
	pc, err := s.newPeerConnection()
	if err != nil {
		return nil, err
	}
	// Recvonly audio transceiver so the server's offer invites the client's mic
	// (the server is the sole offerer). Screen-share video gets a recvonly video
	// transceiver in M5.
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("add recvonly audio: %w", err)
	}
	p := &Peer{
		pid:    pid,
		roomID: roomID,
		pc:     pc,
		sig:    sig,
		sfu:    s,
		log:    s.log.With("room", roomID, "participant", pid),
		subs:   make(map[room.ParticipantID]map[*webrtc.TrackLocalStaticRTP]*webrtc.RTPSender),
		recv:   map[webrtc.RTPCodecType]bool{webrtc.RTPCodecTypeAudio: true}, // recvonly audio added above
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if err := sig.SendCandidate(c.ToJSON()); err != nil {
			p.log.Warn("send candidate", "err", err)
		}
	})
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		s.onPublish(p, remote)
	})
	pc.OnICEConnectionStateChange(func(st webrtc.ICEConnectionState) {
		p.log.Debug("ice state", "state", st.String())
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		p.log.Debug("pc state", "state", st.String())
		switch st {
		case webrtc.PeerConnectionStateConnected:
			if p.OnConnected != nil {
				p.OnConnected()
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			// Only the CURRENT peer's transport loss matters — a replaced (stale)
			// peer's PC closing during a reattach must NOT touch the slot. And we do
			// NOT tear the publication down here: that's the room's job via the grace
			// window (OnClosed → participant.Drop → eventual SFU.Leave on the left
			// event). Tearing down here would defeat refresh resumption.
			if s.currentPeer(roomID, pid) == p && p.OnClosed != nil {
				p.OnClosed()
			}
		}
	})

	s.mu.Lock()
	r := s.rooms[roomID]
	if r == nil {
		r = &sfuRoom{
			peers:    make(map[room.ParticipantID]*Peer),
			pubs:     make(map[room.ParticipantID]*publication),
			audience: make(map[room.ParticipantID]*audience),
		}
		s.rooms[roomID] = r
	}
	old := r.peers[pid]
	r.peers[pid] = p
	s.mu.Unlock()

	// Reattach: drop the old transport but keep the publication + subscribers.
	if old != nil {
		old.markReplaced()
		_ = old.pc.Close()
		p.log.Info("participant reattached (fresh PC, publication kept)")
	}
	return p, nil
}

// keyframeFor returns the keyframe-request func for a fan-out track (nil for
// audio / unknown tracks).
func (s *SFU) keyframeFor(track *webrtc.TrackLocalStaticRTP) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyframe[track]
}

// currentPeer returns the peer currently registered for (room, pid), or nil.
func (s *SFU) currentPeer(roomID room.RoomID, pid room.ParticipantID) *Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[roomID]
	if r == nil {
		return nil
	}
	return r.peers[pid]
}

// publicationsExcept snapshots every OTHER participant's fan-out tracks, so a
// peer can wire its subscriptions without taking another peer's lock.
func (s *SFU) publicationsExcept(roomID room.RoomID, pid room.ParticipantID) []sourcePub {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[roomID]
	if r == nil {
		return nil
	}
	var out []sourcePub
	for opid, pub := range r.pubs {
		if opid == pid {
			continue
		}
		if !r.audienceHears(opid, pid) {
			continue // supervisor gating: this subscriber isn't in the publisher's audience
		}
		tracks := make([]*webrtc.TrackLocalStaticRTP, 0, len(pub.tracks))
		for _, t := range pub.tracks {
			tracks = append(tracks, t)
		}
		if len(tracks) > 0 {
			out = append(out, sourcePub{pid: opid, tracks: tracks})
		}
	}
	return out
}

// audienceDecision is one existing peer and whether it should now hear pid.
type audienceDecision struct {
	q    *Peer
	hear bool
}

// setAudienceState records pid's audience for the room and returns, for every
// OTHER live peer, whether it should now hear pid — plus whether pid currently
// has a publication to wire. Pure w.r.t. media: it only mutates the audience map
// and snapshots peers. SetAudience (Task 2) turns the decisions into add/remove.
func (s *SFU) setAudienceState(roomID room.RoomID, pid room.ParticipantID, all bool, allowed []room.ParticipantID) (decisions []audienceDecision, hasPub bool) {
	aud := &audience{all: all}
	if !all {
		aud.allowed = make(map[room.ParticipantID]bool, len(allowed))
		for _, a := range allowed {
			aud.allowed[a] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[roomID]
	if r == nil {
		return nil, false
	}
	r.audience[pid] = aud
	_, hasPub = r.pubs[pid]
	for opid, q := range r.peers {
		if opid == pid {
			continue
		}
		decisions = append(decisions, audienceDecision{q: q, hear: aud.hears(opid)})
	}
	return decisions, hasPub
}

// SetAudience sets who may hear pid and re-wires live subscribers to match:
// peers that should now hear pid but don't get Negotiate()d (publicationsExcept
// now includes pid); peers that must no longer hear pid have pid's tracks
// removed. Safe to call before pid has published — it just records the audience,
// which the eventual publish honors via publicationsExcept. This is the single
// entry point for monitor (all=false, allowed=nil), whisper (all=false,
// allowed=agent pids) and barge (all=true).
func (s *SFU) SetAudience(roomID room.RoomID, pid room.ParticipantID, all bool, allowed []room.ParticipantID) {
	decisions, hasPub := s.setAudienceState(roomID, pid, all, allowed)
	if !hasPub {
		return // nothing wired yet; publish-time Negotiate will honor the audience
	}
	for _, d := range decisions {
		if d.hear {
			d.q.Negotiate() // subscribe: publicationsExcept now yields pid
		} else {
			s.unsubscribe(d.q, pid) // drop pid's tracks from this subscriber
		}
	}
}

// unsubscribe removes every track of source pid from subscriber q and
// renegotiates q. No-op if q wasn't subscribed. Mirrors the per-subscriber
// teardown in Unpublish/Leave.
func (s *SFU) unsubscribe(q *Peer, pid room.ParticipantID) {
	q.mu.Lock()
	byTrack := q.subs[pid]
	if len(byTrack) == 0 {
		q.mu.Unlock()
		return
	}
	senders := make([]*webrtc.RTPSender, 0, len(byTrack))
	for _, sender := range byTrack {
		senders = append(senders, sender)
	}
	delete(q.subs, pid)
	q.dirty = true
	q.mu.Unlock()
	for _, sender := range senders {
		if err := q.pc.RemoveTrack(sender); err != nil {
			q.log.Debug("remove track on audience change", "err", err)
		}
	}
	q.Negotiate()
}

// onPublish copies a publisher's remote RTP into its room-owned fan-out track,
// creating the track on first publish (and then notifying other peers to
// subscribe) or REUSING it on a reconnect republish (subscribers stay bound —
// no renegotiation). The track is keyed by participant + codec kind.
func (s *SFU) onPublish(p *Peer, remote *webrtc.TrackRemote) {
	kind := remote.Kind()

	s.mu.Lock()
	r := s.rooms[p.roomID]
	if r == nil {
		s.mu.Unlock()
		return
	}
	pub := r.pubs[p.pid]
	if pub == nil {
		pub = &publication{tracks: make(map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP)}
		r.pubs[p.pid] = pub
	}
	track := pub.tracks[kind]
	fresh := track == nil
	if fresh {
		var err error
		track, err = webrtc.NewTrackLocalStaticRTP(
			remote.Codec().RTPCodecCapability,
			"t-"+uuid.NewString(), string(p.pid),
		)
		if err != nil {
			s.mu.Unlock()
			p.log.Error("create fan-out track", "err", err)
			return
		}
		pub.tracks[kind] = track
		// Video subscribers need a keyframe to start decoding. Record how to ask
		// THIS publisher for one (PLI to its PC for this track's SSRC); the SFU
		// calls it on subscribe and when a subscriber sends a PLI.
		if kind == webrtc.RTPCodecTypeVideo {
			pubPC, ssrc := p.pc, uint32(remote.SSRC())
			s.keyframe[track] = func() {
				_ = pubPC.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
			}
		}
	}
	var others []*Peer
	var ports []*roomPort
	if fresh {
		for opid, q := range r.peers {
			if opid != p.pid {
				others = append(others, q)
			}
		}
		ports = s.portsForLocked(r, p.pid)
	}
	s.mu.Unlock()

	// Ports (phone/WhatsApp legs) hear every publisher — attach them as sinks on
	// a fresh publication so the external caller hears this participant.
	for _, port := range ports {
		pub.addSink(port)
	}

	p.log.Info("participant published", "codec", remote.Codec().MimeType, "kind", kind.String(), "fresh", fresh)

	// Only a brand-new publication needs subscribers wired; a reconnect reuses the
	// existing track, so the other peers are already subscribed.
	for _, q := range others {
		q.Negotiate()
	}

	// Copy loop: remote → fan-out track (and tee to any sinks). Ends when the
	// remote track closes (the PC was closed — hangup or a reconnect swap). On a
	// swap, the new PC's OnTrack starts a fresh loop into the SAME track.
	for {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			p.log.Debug("publisher read loop ended", "err", err)
			return
		}
		if err := track.WriteRTP(pkt); err != nil {
			p.log.Debug("fan-out write ended", "err", err)
			return
		}
		teeSinks(pub, p.pid, pkt)
	}
}

// Unpublish stops one of a participant's tracks (e.g. screen-share video ended):
// it drops that kind's fan-out track and removes the matching sender from every
// subscriber, renegotiating them. The participant's other tracks (audio) are
// untouched — they're separate transceivers — so stopping screen share never
// disrupts the call's audio.
func (s *SFU) Unpublish(roomID room.RoomID, pid room.ParticipantID, kind webrtc.RTPCodecType) {
	s.mu.Lock()
	r := s.rooms[roomID]
	if r == nil {
		s.mu.Unlock()
		return
	}
	pub := r.pubs[pid]
	var track *webrtc.TrackLocalStaticRTP
	if pub != nil {
		track = pub.tracks[kind]
		delete(pub.tracks, kind)
		delete(s.keyframe, track)
	}
	var others []*Peer
	for opid, q := range r.peers {
		if opid != pid {
			others = append(others, q)
		}
	}
	s.mu.Unlock()
	if track == nil {
		return
	}
	s.portsClearActive(roomID, pid) // a port locked onto this feed switches over

	for _, q := range others {
		q.mu.Lock()
		sender := q.subs[pid][track]
		if sender != nil {
			delete(q.subs[pid], track)
			q.dirty = true
		}
		q.mu.Unlock()
		if sender != nil {
			if err := q.pc.RemoveTrack(sender); err != nil {
				q.log.Debug("remove track on unpublish", "err", err)
			}
			q.Negotiate()
		}
	}
}

// Leave is the TERMINAL teardown (explicit bye or grace expiry, via the room's
// left event): it removes the participant's peer and publication, removes its
// fan-out tracks from every subscriber (renegotiating them), and closes the PC.
// Idempotent.
func (s *SFU) Leave(roomID room.RoomID, pid room.ParticipantID) {
	s.mu.Lock()
	r := s.rooms[roomID]
	if r == nil {
		s.mu.Unlock()
		return
	}
	p := r.peers[pid]
	if pub := r.pubs[pid]; pub != nil {
		for _, t := range pub.tracks {
			delete(s.keyframe, t)
		}
	}
	delete(r.peers, pid)
	delete(r.pubs, pid)
	if port := r.ports[pid]; port != nil { // a departing port leg
		_ = port.Close()
		delete(r.ports, pid)
	}
	if len(r.peers) == 0 && len(r.pubs) == 0 && len(r.ports) == 0 {
		delete(s.rooms, roomID)
	}
	remaining := make([]*Peer, 0, len(r.peers))
	for _, q := range r.peers {
		remaining = append(remaining, q)
	}
	s.mu.Unlock()
	s.portsClearActive(roomID, pid) // a port locked onto this feed switches over

	// Remove all of this source's tracks from each remaining subscriber, resync.
	for _, q := range remaining {
		q.mu.Lock()
		byTrack := q.subs[pid]
		delete(q.subs, pid)
		senders := make([]*webrtc.RTPSender, 0, len(byTrack))
		for _, sender := range byTrack {
			senders = append(senders, sender)
		}
		if len(senders) > 0 {
			q.dirty = true
		}
		q.mu.Unlock()
		for _, sender := range senders {
			if err := q.pc.RemoveTrack(sender); err != nil {
				q.log.Debug("remove track on leave", "err", err)
			}
		}
		if len(senders) > 0 {
			q.Negotiate()
		}
	}

	if p != nil {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		_ = p.pc.Close()
		if p.OnClosed != nil {
			// Already gone from the slot; OnClosed here would re-Drop a participant
			// that's already leaving. The room left event that triggered Leave is the
			// authority, so we don't call it.
			_ = p.OnClosed
		}
		p.log.Info("participant left (terminal teardown)")
	}
}

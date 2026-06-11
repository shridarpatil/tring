package sfu

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/room"
)

// Join creates a PeerConnection for a participant and registers it in the room.
// The server is the sole offerer: after wiring its client-side handlers, the
// caller calls Peer.Negotiate to send the initial offer, then drives signaling
// through HandleAnswer / HandleCandidate. Set Peer.OnConnected / OnClosed to
// mirror transport state onto the room.Participant.
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
		subs:   make(map[room.ParticipantID][]*webrtc.RTPSender),
	}

	// Outbound trickle ICE.
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if err := sig.SendCandidate(c.ToJSON()); err != nil {
			p.log.Warn("send candidate", "err", err)
		}
	})

	// A client track arrived → publish it to the room.
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
			s.Leave(roomID, pid)
		}
	})

	s.mu.Lock()
	r := s.rooms[roomID]
	if r == nil {
		r = make(map[room.ParticipantID]*Peer)
		s.rooms[roomID] = r
	}
	r[pid] = p
	s.mu.Unlock()
	return p, nil
}

// peersInRoom returns a snapshot of the live peers in a room.
func (s *SFU) peersInRoom(roomID room.RoomID) []*Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[roomID]
	out := make([]*Peer, 0, len(r))
	for _, p := range r {
		out = append(out, p)
	}
	return out
}

// onPublish turns a publisher's remote track into a room-owned fan-out track:
// it copies the remote RTP into a TrackLocalStaticRTP keyed by the participant,
// then renegotiates every OTHER peer in the room so they receive it. The
// fan-out track is owned by the peer slot, so a future reconnect (M4) re-points
// this copy loop into the same track without touching subscribers.
func (s *SFU) onPublish(p *Peer, remote *webrtc.TrackRemote) {
	local, err := webrtc.NewTrackLocalStaticRTP(
		remote.Codec().RTPCodecCapability,
		// Unique track id; stream id = participant so the browser groups a
		// participant's audio+screen tracks together.
		"t-"+uuid.NewString(), string(p.pid),
	)
	if err != nil {
		p.log.Error("create fan-out track", "err", err)
		return
	}
	p.addPublished(local)
	p.log.Info("participant published", "codec", remote.Codec().MimeType, "kind", remote.Kind().String())

	// Wire existing peers to receive it.
	for _, q := range s.peersInRoom(p.roomID) {
		if q.pid != p.pid {
			q.Negotiate()
		}
	}

	// Copy loop: remote → fan-out track. Ends when the remote track closes
	// (ReadRTP errors), e.g. on hangup or transport teardown.
	go func() {
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				p.log.Debug("publisher read loop ended", "err", err)
				return
			}
			if err := local.WriteRTP(pkt); err != nil {
				p.log.Debug("fan-out write ended", "err", err)
				return
			}
		}
	}()
}

// Leave removes a participant: it drops the peer from the room, removes its
// fan-out tracks from every subscriber (and renegotiates them), closes the PC,
// and fires OnClosed. Idempotent — a second call (e.g. state-change after an
// explicit leave) is a no-op.
func (s *SFU) Leave(roomID room.RoomID, pid room.ParticipantID) {
	s.mu.Lock()
	r := s.rooms[roomID]
	p := r[pid]
	if p == nil {
		s.mu.Unlock()
		return
	}
	delete(r, pid)
	if len(r) == 0 {
		delete(s.rooms, roomID)
	}
	remaining := make([]*Peer, 0, len(r))
	for _, q := range r {
		remaining = append(remaining, q)
	}
	s.mu.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	// Remove this source's tracks from each remaining subscriber, then resync.
	for _, q := range remaining {
		q.mu.Lock()
		senders := q.subs[pid]
		delete(q.subs, pid)
		if len(senders) > 0 {
			q.dirty = true // removal needs a renegotiation to take effect
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

	_ = p.pc.Close()
	if p.OnClosed != nil {
		p.OnClosed()
	}
	p.log.Info("participant left")
}

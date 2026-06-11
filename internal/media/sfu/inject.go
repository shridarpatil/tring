package sfu

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/room"
)

// InjectSource publishes a NON-WebRTC participant into a room: a
// room.ParticipantSource (Opus RTP from outside WebRTC — the IVR prompt player
// in M8, the FreeSWITCH phone leg in Phase 2) appears as an ordinary
// publication, forwarded to every browser exactly like a browser publisher.
// The room can't tell the difference — that's the whole point of the seam.
//
// It runs a copy loop (src.ReadRTP → fan-out track + sinks) until the source
// errors/closes. cap is the source's codec (e.g. Opus 48k stereo); kind is
// derived from it. The returned ParticipantID is the source's id.
func (s *SFU) InjectSource(roomID room.RoomID, src room.ParticipantSource, cap webrtc.RTPCodecCapability) error {
	kind := webrtc.RTPCodecTypeAudio
	if isVideoMime(cap.MimeType) {
		kind = webrtc.RTPCodecTypeVideo
	}
	track, err := webrtc.NewTrackLocalStaticRTP(cap, "t-"+uuid.NewString(), string(src.ID()))
	if err != nil {
		return fmt.Errorf("inject track: %w", err)
	}

	s.mu.Lock()
	r := s.rooms[roomID]
	if r == nil {
		r = &sfuRoom{
			peers: make(map[room.ParticipantID]*Peer),
			pubs:  make(map[room.ParticipantID]*publication),
		}
		s.rooms[roomID] = r
	}
	pub := r.pubs[src.ID()]
	if pub == nil {
		pub = &publication{tracks: make(map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP)}
		r.pubs[src.ID()] = pub
	}
	pub.tracks[kind] = track
	others := make([]*Peer, 0, len(r.peers))
	for opid, q := range r.peers {
		if opid != src.ID() {
			others = append(others, q)
		}
	}
	s.mu.Unlock()

	s.log.Info("source injected", "room", roomID, "participant", src.ID(), "codec", cap.MimeType)
	for _, q := range others {
		q.Negotiate() // subscribe existing peers to the injected source
	}

	go func() {
		for {
			pkt, err := src.ReadRTP()
			if err != nil {
				s.log.Debug("injected source read ended", "participant", src.ID(), "err", err)
				return
			}
			if err := track.WriteRTP(pkt); err != nil {
				return
			}
			teeSinks(pub, src.ID(), pkt)
		}
	}()
	return nil
}

// RemoveSource tears down an injected source: drops its publication and removes
// its track from every subscriber (renegotiating them). Mirrors Leave for a
// participant that has publications but no Peer.
func (s *SFU) RemoveSource(roomID room.RoomID, pid room.ParticipantID) {
	s.Leave(roomID, pid)
}

// AddSink attaches a downstream consumer of a participant's RTP (recording, an
// STT tap, an IVR's ear). The publication is created if the participant hasn't
// published yet, so a sink can be attached before media starts.
func (s *SFU) AddSink(roomID room.RoomID, pid room.ParticipantID, sink room.TrackSink) {
	s.mu.Lock()
	r := s.rooms[roomID]
	if r == nil {
		r = &sfuRoom{
			peers: make(map[room.ParticipantID]*Peer),
			pubs:  make(map[room.ParticipantID]*publication),
		}
		s.rooms[roomID] = r
	}
	pub := r.pubs[pid]
	if pub == nil {
		pub = &publication{tracks: make(map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP)}
		r.pubs[pid] = pub
	}
	s.mu.Unlock()
	pub.addSink(sink)
}

// addSink appends a sink, swapping the atomic slice so the copy loop sees it
// without locking.
func (p *publication) addSink(sink room.TrackSink) {
	for {
		old := p.sinks.Load()
		var next []room.TrackSink
		if old != nil {
			next = append(next, *old...)
		}
		next = append(next, sink)
		if p.sinks.CompareAndSwap(old, &next) {
			return
		}
	}
}

// teeSinks forwards a packet to every sink attached to a publication (no-op if
// none). Called from the hot copy loop, so it only does an atomic load.
func teeSinks(pub *publication, pid room.ParticipantID, pkt *rtp.Packet) {
	if sp := pub.sinks.Load(); sp != nil {
		for _, sk := range *sp {
			_ = sk.WriteRTP(pid, pkt)
		}
	}
}

func isVideoMime(mime string) bool {
	return mime == webrtc.MimeTypeVP8 || mime == webrtc.MimeTypeVP9 || mime == webrtc.MimeTypeH264 || mime == webrtc.MimeTypeAV1
}

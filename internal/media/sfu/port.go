package sfu

import (
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/room"
)

// A port is a BIDIRECTIONAL non-WebRTC leg in a room — the seam every external
// call source (FreeSWITCH phone leg, a future sipgo trunk, WhatsApp) plugs into:
//
//	in:  the leg's audio enters the room as an ordinary ParticipantSource
//	     (InjectSource — peers subscribe to it like any publisher).
//	out: the ROOM's audio flows back to the leg. The port registers itself as a
//	     TrackSink on every other publication (current and future), forwarding
//	     ONE active publisher at a time onto a continuous RTP stream.
//
// One-active-feed is deliberate: the out stream is a single RTP flow (one SSRC),
// and interleaving two simultaneous publishers without decoding would garble.
// For call-center calls (caller ↔ IVR, then caller ↔ agent) one speaker stream
// at a time is correct; true N-party mixing toward PSTN needs an Opus mixer and
// is future work. Switchover happens when the active publisher unpublishes or
// leaves (e.g. the IVR voice is removed after a transfer → the agent's audio
// takes over).
type roomPort struct {
	pid   room.ParticipantID // the port's own participant id (never echoed back)
	write func(*rtp.Packet) error

	mu     sync.Mutex
	active room.ParticipantID
	gone   map[room.ParticipantID]struct{} // removed publishers; their copy-loop tail packets must not re-claim the feed
	seq    uint16
	ts     uint32
	closed bool
}

// WriteRTP implements room.TrackSink: forward the active publisher's packets to
// the leg, re-stamped onto the port's own continuous seq/ts (the leg sees one
// clean RTP stream across switchovers). 20ms Opus framing assumed (our planes').
func (p *roomPort) WriteRTP(from room.ParticipantID, pkt *rtp.Packet) error {
	p.mu.Lock()
	if p.closed || from == p.pid {
		p.mu.Unlock()
		return nil
	}
	if _, dead := p.gone[from]; dead {
		p.mu.Unlock()
		return nil // a removed publisher's lingering tail — never re-claim
	}
	if p.active == "" {
		p.active = from
	}
	if from != p.active {
		p.mu.Unlock()
		return nil // not the active feed
	}
	p.seq++
	p.ts += opusFrameSamplesPort
	out := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: pkt.PayloadType,
			SequenceNumber: p.seq, Timestamp: p.ts, SSRC: portSSRC,
		},
		Payload: pkt.Payload,
	}
	p.mu.Unlock()
	return p.write(out)
}

// Close implements room.TrackSink. Idempotent; a closed port drops writes.
func (p *roomPort) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

// clearActive releases the active feed if it was `gone` (so the next publisher
// — e.g. the agent after the IVR leaves — takes over) and bans `gone` so its
// copy-loop tail packets can't re-claim the slot.
func (p *roomPort) clearActive(gone room.ParticipantID) {
	p.mu.Lock()
	if p.gone == nil {
		p.gone = make(map[room.ParticipantID]struct{})
	}
	p.gone[gone] = struct{}{}
	if p.active == gone {
		p.active = ""
	}
	p.mu.Unlock()
}

const (
	// opusFrameSamplesPort is the RTP timestamp increment per 20ms Opus frame.
	opusFrameSamplesPort = 960
	// portSSRC is the fixed SSRC of a port's outbound stream.
	portSSRC = 0x7064 // "pd"
)

// AttachPort plugs a bidirectional external leg into a room: src publishes the
// leg's audio into the room, and `out` receives the room's audio (one active
// publisher at a time — see roomPort). Returns the port participant id (= src's)
// for DetachPort.
func (s *SFU) AttachPort(roomID room.RoomID, src room.ParticipantSource, out func(*rtp.Packet) error) error {
	port := &roomPort{pid: src.ID(), write: out}

	s.mu.Lock()
	r := s.rooms[roomID]
	if r == nil {
		r = &sfuRoom{
			peers: make(map[room.ParticipantID]*Peer),
			pubs:  make(map[room.ParticipantID]*publication),
		}
		s.rooms[roomID] = r
	}
	if r.ports == nil {
		r.ports = make(map[room.ParticipantID]*roomPort)
	}
	r.ports[src.ID()] = port
	// Hear every EXISTING publication (future ones attach in onPublish/InjectSource).
	existing := make([]*publication, 0, len(r.pubs))
	for pid, pub := range r.pubs {
		if pid != src.ID() {
			existing = append(existing, pub)
		}
	}
	s.mu.Unlock()
	for _, pub := range existing {
		pub.addSink(port)
	}

	// And publish the leg's audio into the room.
	return s.InjectSource(roomID, src, webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	})
}

// DetachPort removes a port: stops its outbound feed and removes its publication
// from the room (peers renegotiate). The leg's owner closes the source/socket.
func (s *SFU) DetachPort(roomID room.RoomID, pid room.ParticipantID) {
	s.mu.Lock()
	r := s.rooms[roomID]
	var port *roomPort
	if r != nil && r.ports != nil {
		port = r.ports[pid]
		delete(r.ports, pid)
	}
	s.mu.Unlock()
	if port != nil {
		_ = port.Close()
	}
	s.RemoveSource(roomID, pid)
}

// attachPortsLocked subscribes every room port (except the publisher itself) as
// a sink on a fresh publication. Caller holds s.mu; sinks are attached after
// unlock by the caller via the returned list.
func (s *SFU) portsForLocked(r *sfuRoom, publisher room.ParticipantID) []*roomPort {
	if r == nil || r.ports == nil {
		return nil
	}
	out := make([]*roomPort, 0, len(r.ports))
	for pid, port := range r.ports {
		if pid != publisher {
			out = append(out, port)
		}
	}
	return out
}

// portsClearActive releases `gone` as the active feed on every port in the room
// (called on unpublish/leave so the next publisher takes over).
func (s *SFU) portsClearActive(roomID room.RoomID, gone room.ParticipantID) {
	s.mu.Lock()
	r := s.rooms[roomID]
	var ports []*roomPort
	if r != nil && r.ports != nil {
		ports = make([]*roomPort, 0, len(r.ports))
		for _, p := range r.ports {
			ports = append(ports, p)
		}
	}
	s.mu.Unlock()
	for _, p := range ports {
		p.clearActive(gone)
	}
}

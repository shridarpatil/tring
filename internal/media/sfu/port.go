package sfu

import (
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
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
// at a time is correct. Switchover happens when the active publisher unpublishes
// or leaves (e.g. the IVR voice is removed after a transfer → the agent's audio
// takes over). While a supervisor barges, the port mixes instead (mix.go): every
// publisher it hears is decoded, summed and re-encoded onto the same stream.
type roomPort struct {
	pid   room.ParticipantID // the port's own participant id (never echoed back)
	write func(*rtp.Packet) error

	mu     sync.Mutex
	active room.ParticipantID
	pinned room.ParticipantID // when set, the only feed forwarded (hold music) until it leaves
	gone   map[room.ParticipantID]struct{} // removed publishers; their copy-loop tail packets must not re-claim the feed
	seq    uint16
	ts     uint32
	closed bool

	barging map[room.ParticipantID]struct{} // supervisors barging in; the port mixes while any are
	mixer   *portMixer                      // non-nil while mixing
	pt      uint8                           // payload type for mixed packets (the publishers')
	stopMix chan struct{}                   // ends the mixing loop
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
	if p.mixer != nil {
		p.pt = pkt.PayloadType
		p.mixer.push(from, pkt.Payload)
		p.mu.Unlock()
		return nil // mixLoop sends the mix
	}
	if p.pinned != "" {
		p.active = p.pinned // a pinned feed (hold music) preempts whoever was talking
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
	p.stopMixingLocked()
	p.mu.Unlock()
	return nil
}

// setBarge records whether supervisor pid is barging. The port mixes while any
// supervisor is, and goes back to forwarding one publisher when the last stops.
// Returns false if mixing was wanted but this binary can't (no libopus).
func (p *roomPort) setBarge(pid room.ParticipantID, on bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on {
		if p.barging == nil {
			p.barging = make(map[room.ParticipantID]struct{})
		}
		p.barging[pid] = struct{}{}
	} else {
		delete(p.barging, pid)
	}
	if len(p.barging) == 0 {
		p.stopMixingLocked()
		return true
	}
	if p.mixer != nil || p.closed {
		return true
	}
	m, err := newPortMixer()
	if m == nil || err != nil {
		return false
	}
	p.mixer, p.stopMix = m, make(chan struct{})
	go p.mixLoop(m, p.stopMix)
	return true
}

// stopMixingLocked ends mixing; the next packet to arrive claims the single
// feed again (the agent, once the supervisor stopped barging). Caller holds p.mu.
func (p *roomPort) stopMixingLocked() {
	if p.mixer == nil {
		return
	}
	close(p.stopMix)
	p.mixer, p.stopMix = nil, nil
	p.active = ""
}

// mixLoop sends one mixed frame every 20ms, on the port's own seq/ts/SSRC so
// the leg sees one unbroken stream across the switch to and from mixing.
func (p *roomPort) mixLoop(m *portMixer, stop chan struct{}) {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		p.mu.Lock()
		if p.mixer != m {
			p.mu.Unlock()
			return
		}
		payload, ok := m.frame()
		if !ok {
			p.mu.Unlock()
			continue
		}
		p.seq++
		p.ts += opusFrameSamplesPort
		out := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: p.pt,
				SequenceNumber: p.seq, Timestamp: p.ts, SSRC: portSSRC,
			},
			Payload: payload,
		}
		p.mu.Unlock()
		_ = p.write(out)
	}
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
	if p.pinned == gone {
		p.pinned = ""
	}
	if p.active == gone {
		p.active = ""
	}
	if p.mixer != nil {
		p.mixer.drop(gone)
	}
	p.mu.Unlock()
	p.setBarge(gone, false) // a barging supervisor left or was cut off
}

// restore lifts clearActive's ban on a publisher, so a publisher the port hears
// again (a supervisor barging a second time) can claim the feed.
func (p *roomPort) restore(pid room.ParticipantID) {
	p.mu.Lock()
	delete(p.gone, pid)
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
			peers:    make(map[room.ParticipantID]*Peer),
			pubs:     make(map[room.ParticipantID]*publication),
			audience: make(map[room.ParticipantID]*audience),
		}
		s.rooms[roomID] = r
	}
	if r.ports == nil {
		r.ports = make(map[room.ParticipantID]*roomPort)
	}
	r.ports[src.ID()] = port
	// Hear every EXISTING publication its audience admits (future ones attach in
	// onPublish/InjectSource; audience changes in SetAudience).
	for pid, pub := range r.pubs {
		if pid != src.ID() && r.audienceHears(pid, port.pid) {
			pub.addSinkOnce(port)
		}
	}
	s.mu.Unlock()

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

// attachPortsLocked subscribes every room port (except the publisher itself)
// that the publisher's audience admits as a sink on its publication — so a
// supervisor in monitor/whisper never reaches the phone caller. Caller holds
// s.mu, which keeps it ordered against SetAudience's re-wiring.
func (s *SFU) attachPortsLocked(r *sfuRoom, publisher room.ParticipantID, pub *publication) {
	for pid, port := range r.ports {
		if pid != publisher && r.audienceHears(publisher, pid) {
			pub.addSinkOnce(port)
		}
	}
}

// HasPort reports whether a room has an attached external leg (a phone/WhatsApp
// caller). Such a room is "live" even with zero WebRTC participants — e.g. the
// IVR must not wait for a browser to connect before its first prompt.
func (s *SFU) HasPort(roomID room.RoomID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[roomID]
	return r != nil && len(r.ports) > 0
}

// PinPortFeed makes pid the only feed every port in the room forwards, until
// pid leaves (RemoveSource/Leave). Used for agent hold music on a phone call:
// the agent's muted track keeps sending (silent) packets, so without the pin it
// stays the leg's feed and the caller hears silence instead of the music.
func (s *SFU) PinPortFeed(roomID room.RoomID, pid room.ParticipantID) {
	for _, p := range s.roomPorts(roomID) {
		p.mu.Lock()
		p.pinned = pid
		p.mu.Unlock()
	}
}

// ReleasePortFeed frees every port's forwarded feed (without banning it) so the
// next publisher to send claims it. A transfer's answered agent needs this: the
// IVR's voice stays the phone leg's feed after its last prompt, idle but still
// active, so the caller would never hear the agent while the IVR waits out the
// call. The idle IVR can claim the feed back when it next plays.
func (s *SFU) ReleasePortFeed(roomID room.RoomID) {
	for _, p := range s.roomPorts(roomID) {
		p.mu.Lock()
		if p.pinned == "" {
			p.active = ""
		}
		p.mu.Unlock()
	}
}

// roomPorts snapshots a room's ports.
func (s *SFU) roomPorts(roomID room.RoomID) []*roomPort {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[roomID]
	if r == nil || r.ports == nil {
		return nil
	}
	ports := make([]*roomPort, 0, len(r.ports))
	for _, p := range r.ports {
		ports = append(ports, p)
	}
	return ports
}

// portsClearActive releases `gone` as the active feed on every port in the room
// (called on unpublish/leave so the next publisher takes over).
func (s *SFU) portsClearActive(roomID room.RoomID, gone room.ParticipantID) {
	for _, p := range s.roomPorts(roomID) {
		p.clearActive(gone)
	}
}

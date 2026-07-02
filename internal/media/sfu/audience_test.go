package sfu

import (
	"testing"

	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
)

// dummyTrack makes a real (pure, no network) fan-out track so a publication is
// non-empty and thus visible to publicationsExcept.
func dummyTrack(t *testing.T, id string) *webrtc.TrackLocalStaticRTP {
	t.Helper()
	tr, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"t-"+id, id)
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	return tr
}

func (s *SFU) putPub(t *testing.T, roomID room.RoomID, pid room.ParticipantID) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[roomID]
	if r == nil {
		r = &sfuRoom{
			peers:    map[room.ParticipantID]*Peer{},
			pubs:     map[room.ParticipantID]*publication{},
			audience: map[room.ParticipantID]*audience{},
		}
		s.rooms[roomID] = r
	}
	r.pubs[pid] = &publication{tracks: map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP{
		webrtc.RTPCodecTypeAudio: dummyTrack(t, string(pid)),
	}}
}

func hasSource(pubs []sourcePub, pid room.ParticipantID) bool {
	for _, p := range pubs {
		if p.pid == pid {
			return true
		}
	}
	return false
}

func TestAudienceHears(t *testing.T) {
	var everyone *audience // nil ⇒ everyone
	if !everyone.hears("anyone") {
		t.Fatal("nil audience must be audible to everyone")
	}
	all := &audience{all: true}
	if !all.hears("anyone") {
		t.Fatal("all=true must be audible to everyone")
	}
	nobody := &audience{allowed: map[room.ParticipantID]bool{}}
	if nobody.hears("agent") {
		t.Fatal("empty allow-set must be audible to no one")
	}
	whisper := &audience{allowed: map[room.ParticipantID]bool{"agent": true}}
	if !whisper.hears("agent") || whisper.hears("customer") {
		t.Fatal("whisper must reach only the allowed pid")
	}
}

func TestPublicationsExceptHonorsAudience(t *testing.T) {
	s, err := New(Config{IncludeLoopback: true})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	const rid = room.RoomID("t1/room")
	s.putPub(t, rid, "sup")
	s.putPub(t, rid, "agent")
	s.putPub(t, rid, "customer")

	// Default (no audience entry): supervisor pub is audible to everyone.
	if !hasSource(s.publicationsExcept(rid, "customer"), "sup") {
		t.Fatal("default audience should expose sup to customer")
	}

	// Monitor: nobody hears sup.
	s.setAudienceState(rid, "sup", false, nil)
	if hasSource(s.publicationsExcept(rid, "customer"), "sup") {
		t.Fatal("monitor must hide sup from customer")
	}
	if hasSource(s.publicationsExcept(rid, "agent"), "sup") {
		t.Fatal("monitor must hide sup from agent")
	}
	// Supervisor still hears the others regardless of its own audience.
	if !hasSource(s.publicationsExcept(rid, "sup"), "agent") {
		t.Fatal("supervisor must still hear the agent")
	}

	// Whisper: only the agent hears sup.
	s.setAudienceState(rid, "sup", false, []room.ParticipantID{"agent"})
	if !hasSource(s.publicationsExcept(rid, "agent"), "sup") {
		t.Fatal("whisper must expose sup to agent")
	}
	if hasSource(s.publicationsExcept(rid, "customer"), "sup") {
		t.Fatal("whisper must hide sup from customer")
	}

	// Barge: everyone hears sup again.
	s.setAudienceState(rid, "sup", true, nil)
	if !hasSource(s.publicationsExcept(rid, "customer"), "sup") {
		t.Fatal("barge must expose sup to customer")
	}
}

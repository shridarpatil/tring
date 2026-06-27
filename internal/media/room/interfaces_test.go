package room_test

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/room"
)

// Stub implementations prove the four extension interfaces are real, usable
// seams (M1 defines them; later phases implement them for real). The
// compile-time assertions below fail the build if a signature drifts.

type stubSink struct{ writes int }

func (s *stubSink) WriteRTP(_ room.ParticipantID, _ *rtp.Packet) error { s.writes++; return nil }
func (s *stubSink) Close() error                                       { return nil }

type stubSource struct {
	id   room.ParticipantID
	pkts []*rtp.Packet
}

func (s *stubSource) ID() room.ParticipantID { return s.id }
func (s *stubSource) ReadRTP() (*rtp.Packet, error) {
	if len(s.pkts) == 0 {
		return nil, errors.New("eof")
	}
	p := s.pkts[0]
	s.pkts = s.pkts[1:]
	return p, nil
}
func (s *stubSource) Close() error { return nil }

type stubSupervisor struct{ subscribed room.RoomID }

func (s *stubSupervisor) Subscribe(r room.RoomID) error { s.subscribed = r; return nil }

type stubGateway struct {
	inbound func(room.InboundCall)
}

func (g *stubGateway) OnInbound(f func(room.InboundCall)) { g.inbound = f }
func (g *stubGateway) Answer(room.CallID) (*room.Leg, error) {
	return &room.Leg{Source: &stubSource{id: "pstn"}}, nil
}
func (g *stubGateway) Reject(room.CallID) error { return nil }
func (g *stubGateway) Hangup(room.CallID) error { return nil }

var (
	_ room.TrackSink         = (*stubSink)(nil)
	_ room.ParticipantSource = (*stubSource)(nil)
	_ room.Supervisor        = (*stubSupervisor)(nil)
	_ room.Plug              = (*stubGateway)(nil)
)

func TestExtensionInterfaceStubs(t *testing.T) {
	sink := &stubSink{}
	if err := sink.WriteRTP("p1", &rtp.Packet{}); err != nil {
		t.Fatalf("sink write: %v", err)
	}
	if sink.writes != 1 {
		t.Fatalf("writes = %d, want 1", sink.writes)
	}

	src := &stubSource{id: "ivr", pkts: []*rtp.Packet{{}, {}}}
	if src.ID() != "ivr" {
		t.Fatalf("source id = %q", src.ID())
	}
	if _, err := src.ReadRTP(); err != nil {
		t.Fatalf("source read: %v", err)
	}

	var sup room.Supervisor = &stubSupervisor{}
	if err := sup.Subscribe("call-1"); err != nil {
		t.Fatalf("supervise: %v", err)
	}

	var gw room.Plug = &stubGateway{}
	gw.OnInbound(func(room.InboundCall) {})
	leg, err := gw.Answer("c1")
	if err != nil || leg == nil || leg.Source == nil {
		t.Fatalf("answer: leg=%v err=%v", leg, err)
	}
}

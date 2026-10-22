//go:build opus

package sfu

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
)

// sineSource publishes `frames` 20ms Opus frames of a sine tone, paced in real
// time like a microphone.
type sineSource struct {
	id   room.ParticipantID
	pkts [][]byte
	mu   sync.Mutex
	i    int
}

func newSineSource(t *testing.T, id room.ParticipantID, hz float64, frames int) *sineSource {
	t.Helper()
	enc, err := newOpusEncoder()
	if err != nil {
		t.Fatalf("encoder: %v", err)
	}
	src := &sineSource{id: id}
	pcm := make([]int16, mixFrame)
	buf := make([]byte, 1500)
	for f := range frames {
		for i := range pcm {
			n := float64(f*mixFrame + i)
			pcm[i] = int16(8000 * math.Sin(2*math.Pi*hz*n/48000))
		}
		n, err := enc.Encode(pcm, buf)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		src.pkts = append(src.pkts, append([]byte(nil), buf[:n]...))
	}
	return src
}

func (s *sineSource) ID() room.ParticipantID { return s.id }
func (s *sineSource) Close() error           { return nil }
func (s *sineSource) ReadRTP() (*rtp.Packet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.pkts) {
		return nil, errors.New("done")
	}
	time.Sleep(20 * time.Millisecond)
	s.i++
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: uint16(s.i)},
		Payload: s.pkts[s.i-1],
	}, nil
}

type silentLeg struct{ id room.ParticipantID }

func (l silentLeg) ID() room.ParticipantID        { return l.id }
func (l silentLeg) Close() error                  { return nil }
func (l silentLeg) ReadRTP() (*rtp.Packet, error) { select {} }

// goertzel returns the power of one frequency in a 48kHz signal.
func goertzel(x []int16, hz float64) float64 {
	k := 2 * math.Cos(2*math.Pi*hz/48000)
	var s1, s2 float64
	for _, v := range x {
		s1, s2 = float64(v)+k*s1-s2, s1
	}
	return s1*s1 + s2*s2 - k*s1*s2
}

// TestBargeMixesAgentAndSupervisor is the end-to-end barge contract with real
// libopus: a phone port in a room with an agent (440Hz) and a barging
// supervisor (1kHz) carries both tones.
func TestBargeMixesAgentAndSupervisor(t *testing.T) {
	s, err := New(Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	var mu sync.Mutex
	var got [][]byte
	if err := s.AttachPort("call", silentLeg{id: "phone"}, func(p *rtp.Packet) error {
		mu.Lock()
		got = append(got, p.Payload)
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("attach port: %v", err)
	}
	opusCap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
	if err := s.InjectSource("call", newSineSource(t, "agent", 440, 60), opusCap); err != nil {
		t.Fatal(err)
	}
	s.SetAudience("call", "sup", true, nil) // barge
	if err := s.InjectSource("call", newSineSource(t, "sup", 1000, 60), opusCap); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1300 * time.Millisecond)

	mu.Lock()
	pkts := got
	mu.Unlock()
	dec, _ := newOpusDecoder()
	var pcm []int16
	buf := make([]int16, maxOpusFrame)
	for _, p := range pkts[len(pkts)/4:] { // skip the start, before both are mixed
		n, err := dec.Decode(p, buf)
		if err != nil {
			t.Fatalf("decode the port's output: %v", err)
		}
		pcm = append(pcm, buf[:n]...)
	}
	agent, sup, none := goertzel(pcm, 440), goertzel(pcm, 1000), goertzel(pcm, 2500)
	t.Logf("%d packets; power at 440Hz %.3g, 1kHz %.3g, 2.5kHz %.3g", len(pkts), agent, sup, none)
	if agent < 100*none || sup < 100*none {
		t.Fatal("the caller does not hear both the agent and the supervisor")
	}
}

// BenchmarkMixTick is the CPU cost of one barged phone call per 20ms: decode the
// agent's and the supervisor's frames, mix, encode.
func BenchmarkMixTick(b *testing.B) {
	enc, _ := newOpusEncoder()
	pcm := make([]int16, mixFrame)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/48000))
	}
	buf := make([]byte, 1500)
	n, _ := enc.Encode(pcm, buf)
	pkt := buf[:n]
	m, _ := newPortMixer()
	for range 2 {
		m.push("agent", pkt)
		m.push("sup", pkt)
	}
	b.ResetTimer()
	for range b.N {
		m.push("agent", pkt)
		m.push("sup", pkt)
		if _, ok := m.frame(); !ok {
			b.Fatal("no frame")
		}
	}
}

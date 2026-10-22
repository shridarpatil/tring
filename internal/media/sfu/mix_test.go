package sfu

import (
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
)

// fakeDecoder decodes a packet into one 20ms frame whose every sample is
// 100 × the payload's first byte. fakeEncoder "encodes" a frame as its first
// sample, so a test can read the mixed value back off the wire.
type fakeDecoder struct{}

func (fakeDecoder) Decode(data []byte, pcm []int16) (int, error) {
	for i := range mixFrame {
		pcm[i] = int16(data[0]) * 100
	}
	return mixFrame, nil
}

type fakeEncoder struct{}

func (fakeEncoder) Encode(pcm []int16, data []byte) (int, error) {
	binary.BigEndian.PutUint16(data, uint16(pcm[0]))
	return 2, nil
}

func useFakeOpus(t *testing.T) {
	t.Helper()
	dec, enc := newOpusDecoder, newOpusEncoder
	newOpusDecoder = func() (opusDecoder, error) { return fakeDecoder{}, nil }
	newOpusEncoder = func() (opusEncoder, error) { return fakeEncoder{}, nil }
	t.Cleanup(func() { newOpusDecoder, newOpusEncoder = dec, enc })
}

func mixed(t *testing.T, payload []byte) int16 {
	t.Helper()
	if len(payload) != 2 {
		t.Fatalf("payload %v is not a fake-encoded frame", payload)
	}
	return int16(binary.BigEndian.Uint16(payload))
}

func TestMixerSumsPrimedSources(t *testing.T) {
	useFakeOpus(t)
	m, err := newPortMixer()
	if err != nil || m == nil {
		t.Fatalf("newPortMixer: %v, %v", m, err)
	}

	m.push("agent", []byte{1})
	if _, ok := m.frame(); ok {
		t.Fatal("mixed a source before it buffered mixPrime")
	}
	m.push("agent", []byte{1})
	m.push("sup", []byte{2})
	m.push("sup", []byte{2})
	p, ok := m.frame()
	if !ok || mixed(t, p) != 300 {
		t.Fatalf("frame = %v %v, want agent+sup = 300", p, ok)
	}

	// Each source has one frame left; then both run dry and must re-prime.
	if p, ok := m.frame(); !ok || mixed(t, p) != 300 {
		t.Fatalf("second frame = %v %v, want 300", p, ok)
	}
	if _, ok := m.frame(); ok {
		t.Fatal("mixed with every source dry")
	}
	m.push("agent", []byte{1})
	if _, ok := m.frame(); ok {
		t.Fatal("a source that ran dry was mixed before re-priming")
	}
}

func TestMixerSaturatesAndCapsBacklog(t *testing.T) {
	useFakeOpus(t)
	m, _ := newPortMixer()
	for range 10 { // more than mixMaxBuffered: the oldest frames go
		m.push("a", []byte{200})
		m.push("b", []byte{200})
	}
	if got := len(m.srcs["a"].buf); got != mixMaxBuffered {
		t.Fatalf("backlog = %d samples, want capped at %d", got, mixMaxBuffered)
	}
	p, _ := m.frame()
	if mixed(t, p) != 32767 {
		t.Fatalf("20000+20000 mixed to %d, want clipped to 32767", mixed(t, p))
	}
}

func TestNoMixerWithoutLibopus(t *testing.T) {
	dec, enc := newOpusDecoder, newOpusEncoder
	newOpusDecoder, newOpusEncoder = nil, nil
	t.Cleanup(func() { newOpusDecoder, newOpusEncoder = dec, enc })

	p := &roomPort{pid: "phone", write: func(*rtp.Packet) error { return nil }}
	if p.setBarge("sup", true) {
		t.Fatal("setBarge reported mixing without an Opus codec")
	}
	if p.mixer != nil {
		t.Fatal("port is mixing without an Opus codec")
	}
}

type portOut struct {
	mu   sync.Mutex
	pkts []*rtp.Packet
}

func (o *portOut) write(p *rtp.Packet) error {
	o.mu.Lock()
	o.pkts = append(o.pkts, p)
	o.mu.Unlock()
	return nil
}

func (o *portOut) snapshot() []*rtp.Packet {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*rtp.Packet(nil), o.pkts...)
}

// TestPortPassthroughMixPassthrough: barge switches the port from forwarding
// the agent to sending the agent+supervisor mix, and back, on one continuous
// RTP stream (one SSRC, seq +1, ts +960 per packet).
func TestPortPassthroughMixPassthrough(t *testing.T) {
	useFakeOpus(t)
	out := &portOut{}
	p := &roomPort{pid: "phone", write: out.write}
	send := func(from room.ParticipantID, mark byte) {
		_ = p.WriteRTP(from, &rtp.Packet{Header: rtp.Header{PayloadType: 111}, Payload: []byte{mark}})
	}
	until := func(cond func([]*rtp.Packet) bool, msg string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			send("agent", 1)
			send("sup", 2)
			if cond(out.snapshot()) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(msg)
	}
	isMix := func(pk *rtp.Packet) bool { return len(pk.Payload) == 2 && mixed(t, pk.Payload) == 300 }
	isAgent := func(pk *rtp.Packet) bool { return len(pk.Payload) == 1 && pk.Payload[0] == 1 }

	send("agent", 1) // the agent claims the single feed
	send("sup", 2)
	if pk := out.snapshot(); len(pk) != 1 || !isAgent(pk[0]) {
		t.Fatalf("passthrough sent %v, want only the agent's packet", pk)
	}

	if !p.setBarge("sup", true) {
		t.Fatal("setBarge: no mixer")
	}
	until(func(pk []*rtp.Packet) bool { return isMix(pk[len(pk)-1]) }, "never sent the agent+supervisor mix")

	p.setBarge("sup", false)
	p.clearActive("sup") // the SFU drops the supervisor's sink when barge ends
	n := len(out.snapshot())
	until(func(pk []*rtp.Packet) bool { return len(pk) > n && isAgent(pk[len(pk)-1]) }, "never went back to the agent's feed")
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	pk := out.snapshot()
	for i := 1; i < len(pk); i++ {
		if pk[i].SSRC != pk[0].SSRC {
			t.Fatal("SSRC changed across a mode switch")
		}
		if pk[i].SequenceNumber != pk[i-1].SequenceNumber+1 || pk[i].Timestamp != pk[i-1].Timestamp+opusFrameSamplesPort {
			t.Fatalf("packet %d: seq/ts %d/%d after %d/%d", i, pk[i].SequenceNumber, pk[i].Timestamp, pk[i-1].SequenceNumber, pk[i-1].Timestamp)
		}
	}
}

// pacedSource publishes `total` one-byte packets (payload = mark), 2ms apart.
type pacedSource struct {
	id    room.ParticipantID
	mark  byte
	total int
	mu    sync.Mutex
	sent  int
}

func (f *pacedSource) ID() room.ParticipantID { return f.id }
func (f *pacedSource) Close() error           { return nil }
func (f *pacedSource) ReadRTP() (*rtp.Packet, error) {
	f.mu.Lock()
	if f.sent >= f.total {
		f.mu.Unlock()
		return nil, errors.New("done")
	}
	f.sent++
	f.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111}, Payload: []byte{f.mark}}, nil
}

// TestPortBargeAttachesAndDetaches: barge puts the supervisor into the phone
// caller's mix, leaving barge takes them out, and a second barge puts them back.
func TestPortBargeAttachesAndDetaches(t *testing.T) {
	useFakeOpus(t)
	s, err := New(Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	out := &portOut{}
	if err := s.AttachPort("call", &pacedSource{id: "phone", mark: 9, total: 3000}, out.write); err != nil {
		t.Fatalf("attach port: %v", err)
	}
	opus := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
	s.SetAudience("call", "sup", false, nil)
	if err := s.InjectSource("call", &pacedSource{id: "sup", mark: 3, total: 3000}, opus); err != nil {
		t.Fatal(err)
	}
	supOnly := func() int { // mixed frames carrying the supervisor (fake-encoded 3 × 100)
		n := 0
		for _, p := range out.snapshot() {
			if len(p.Payload) == 2 && mixed(t, p.Payload) == 300 {
				n++
			}
		}
		return n
	}
	waitSup := func(n int, msg string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for supOnly() < n {
			if time.Now().After(deadline) {
				t.Fatal(msg)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	time.Sleep(100 * time.Millisecond)
	if len(out.snapshot()) != 0 {
		t.Fatal("caller hears a monitoring supervisor")
	}
	s.SetAudience("call", "sup", true, nil)
	waitSup(5, "barge did not reach the caller")

	s.SetAudience("call", "sup", false, nil)
	time.Sleep(50 * time.Millisecond)
	n := len(out.snapshot())
	time.Sleep(150 * time.Millisecond)
	if got := len(out.snapshot()); got != n {
		t.Fatalf("caller still hears the supervisor after barge: %d new packets", got-n)
	}

	s.SetAudience("call", "sup", true, nil)
	waitSup(supOnly()+5, "second barge did not reach the caller")
}

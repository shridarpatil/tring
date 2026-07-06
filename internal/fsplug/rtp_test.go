package fsplug

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/shridarpatil/tring/internal/media/room"
)

func dialUDP(t *testing.T, port int) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMediaSessionReadsOpusAndDecodesDTMF(t *testing.T) {
	m, err := newMediaSession(room.ParticipantID("p1"), "127.0.0.1", 12000, 12100, 111, 101)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	fs := dialUDP(t, m.localPort())
	defer fs.Close()

	// Send one Opus packet (PT 111) → ReadRTP must return it.
	opus := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: 1, Timestamp: 960, SSRC: 0x1234}, Payload: []byte{0xfc, 0x01, 0x02}}
	raw, _ := opus.Marshal()
	fs.Write(raw)

	got, err := m.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if got.PayloadType != 111 || len(got.Payload) != 3 {
		t.Fatalf("unexpected opus packet: %+v", got.Header)
	}

	// Send a telephone-event END packet for digit '7' (PT 101) → dtmf() emits "7".
	te := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 101, SequenceNumber: 2, Timestamp: 1920, SSRC: 0x1234}, Payload: []byte{7, 0x8a, 0x00, 0xa0}}
	raw, _ = te.Marshal()
	fs.Write(raw)

	select {
	case d := <-m.dtmf():
		if d != "7" {
			t.Fatalf("dtmf = %q, want 7", d)
		}
	case <-time.After(time.Second):
		t.Fatal("no DTMF emitted")
	}
}

func TestMediaSessionWriteReStampsToRemote(t *testing.T) {
	m, err := newMediaSession(room.ParticipantID("p1"), "127.0.0.1", 12000, 12100, 111, 101)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	fs := dialUDP(t, m.localPort())
	defer fs.Close()
	la := fs.LocalAddr().(*net.UDPAddr)
	m.setRemote("127.0.0.1", la.Port)

	// Write two room packets with arbitrary seq/ssrc; the session must present a
	// clean stream: our fixed SSRC and monotonically increasing sequence.
	for i := 0; i < 2; i++ {
		_ = m.write(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: uint16(9000 + i*50), SSRC: 0xdead}, Payload: []byte{0xaa}})
	}
	fs.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1500)
	var first rtp.Packet
	n, _ := fs.Read(buf)
	first.Unmarshal(buf[:n])
	n, _ = fs.Read(buf)
	var second rtp.Packet
	second.Unmarshal(buf[:n])
	if second.SequenceNumber != first.SequenceNumber+1 {
		t.Fatalf("sequence not monotonic: %d then %d", first.SequenceNumber, second.SequenceNumber)
	}
	if first.SSRC != second.SSRC {
		t.Fatalf("ssrc not stable: %d vs %d", first.SSRC, second.SSRC)
	}
}

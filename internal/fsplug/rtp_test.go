package fsplug

import (
	"bytes"
	"net"
	"sync"
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

// TestMediaSessionNoPayloadAliasingUnderQueueDepth guards against the read
// buffer in readLoop being reused across iterations: pion/rtp's Unmarshal
// sets pkt.Payload as a slice INTO the read buffer, not a copy. If that
// buffer is allocated once outside the loop and reused, a packet still
// sitting in m.in (queue depth > 0) has its payload silently overwritten by
// the next socket read. This sends two packets back-to-back before draining
// so both are queued, then asserts each ReadRTP() call still has its own
// distinct payload bytes.
func TestMediaSessionNoPayloadAliasingUnderQueueDepth(t *testing.T) {
	m, err := newMediaSession(room.ParticipantID("p1"), "127.0.0.1", 12000, 12100, 111, 101)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	fs := dialUDP(t, m.localPort())
	defer fs.Close()

	pktA := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: 1, Timestamp: 960, SSRC: 0x1234}, Payload: []byte{0xAA, 0xAA, 0xAA}}
	pktB := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: 2, Timestamp: 1920, SSRC: 0x1234}, Payload: []byte{0xBB, 0xBB, 0xBB}}

	rawA, _ := pktA.Marshal()
	rawB, _ := pktB.Marshal()
	fs.Write(rawA)
	fs.Write(rawB)

	// Poll until both packets have been queued into m.in before reading any
	// of them, so the aliasing window (queue depth > 0) is actually hit.
	deadline := time.Now().Add(time.Second)
	for len(m.in) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for both packets to queue, got depth %d", len(m.in))
		}
		time.Sleep(time.Millisecond)
	}

	first, err := m.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first.Payload, []byte{0xAA, 0xAA, 0xAA}) {
		t.Fatalf("first packet payload = % x, want AA AA AA (aliased by second read)", first.Payload)
	}
	if !bytes.Equal(second.Payload, []byte{0xBB, 0xBB, 0xBB}) {
		t.Fatalf("second packet payload = % x, want BB BB BB", second.Payload)
	}
}

// TestMediaSessionCloseDuringTrafficNoPanic drives readLoop's
// `case m.in <- pkt` send hard against a concurrent Close() to catch the
// send-on-closed-channel panic race: Close() must never close m.in itself
// (only doneC), otherwise a send racing the close can panic and take down
// the whole gateway process.
func TestMediaSessionCloseDuringTrafficNoPanic(t *testing.T) {
	m, err := newMediaSession(room.ParticipantID("p1"), "127.0.0.1", 12000, 12100, 111, 101)
	if err != nil {
		t.Fatal(err)
	}

	fs := dialUDP(t, m.localPort())
	defer fs.Close()

	stop := make(chan struct{})
	sendDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(sendDone)
		opus := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: 1, Timestamp: 960, SSRC: 0x1234},
			Payload: []byte{0xfc, 0x01, 0x02},
		}
		raw, _ := opus.Marshal()
		for i := 0; i < 500; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := fs.Write(raw); err != nil {
				return
			}
		}
	}()

	// Let a few packets flow before closing, so the race window is live.
	time.Sleep(2 * time.Millisecond)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// select doesn't prefer doneC over a still-buffered m.in, so a packet
	// queued right before Close can legitimately win one more race. Drain
	// until the buffer (capacity 256) is exhausted and doneC is the only
	// ready case; if that never happens within a bounded time, the closed
	// path is broken.
	readErr := make(chan error, 1)
	go func() {
		for i := 0; i < 10000; i++ {
			if _, err := m.ReadRTP(); err != nil {
				readErr <- err
				return
			}
		}
		readErr <- nil
	}()

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("ReadRTP never returned an error after Close (drained 10000 reads)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadRTP did not return after Close (possible hang)")
	}

	close(stop)
	<-sendDone
	wg.Wait()
}

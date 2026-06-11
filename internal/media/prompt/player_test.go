package prompt_test

import (
	"testing"
	"time"

	"github.com/whatomate/tring/internal/media/prompt"
	"github.com/whatomate/tring/internal/media/room"
)

// compile-time: the player is a ParticipantSource (injectable into a room).
var _ room.ParticipantSource = (*prompt.Player)(nil)

func TestPlayerPacketizesFramesWithIncrementingSeqTs(t *testing.T) {
	p := prompt.NewPlayer("ivr", time.Millisecond) // tiny pacing for a fast test
	frames := [][]byte{{1}, {2, 2}, {3, 3, 3}}
	done := p.Play(frames)

	for i, want := range frames {
		pkt, err := p.ReadRTP()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(pkt.Payload) != string(want) {
			t.Fatalf("packet %d payload = %v, want %v", i, pkt.Payload, want)
		}
		if int(pkt.SequenceNumber) != i {
			t.Fatalf("packet %d seq = %d, want %d", i, pkt.SequenceNumber, i)
		}
		if pkt.Timestamp != uint32(i*960) {
			t.Fatalf("packet %d ts = %d, want %d", i, pkt.Timestamp, i*960)
		}
		if pkt.PayloadType != 111 {
			t.Fatalf("packet %d pt = %d, want 111 (opus)", i, pkt.PayloadType)
		}
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("playback done channel never closed after draining frames")
	}
}

func TestPlayerCloseUnblocksRead(t *testing.T) {
	p := prompt.NewPlayer("ivr", time.Millisecond)
	errc := make(chan error, 1)
	go func() {
		_, err := p.ReadRTP() // blocks (no frames) until Close
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = p.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected error after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("ReadRTP did not unblock on Close")
	}
}

func TestPlayEmptyBatchClosesImmediately(t *testing.T) {
	p := prompt.NewPlayer("ivr", time.Millisecond)
	select {
	case <-p.Play(nil):
	case <-time.After(time.Second):
		t.Fatal("empty Play should return an already-closed channel")
	}
}

package gateway

import (
	"testing"

	"github.com/pion/rtp"
)

func TestDtmfDigitMapping(t *testing.T) {
	cases := map[byte]string{
		0: "0", 5: "5", 9: "9", 10: "*", 11: "#", 12: "", 99: "",
	}
	for ev, want := range cases {
		if got := dtmfDigit(ev); got != want {
			t.Errorf("dtmfDigit(%d) = %q, want %q", ev, got, want)
		}
	}
}

func TestPstnSourcePushAfterCloseIsSafe(t *testing.T) {
	s := &pstnSource{id: "pstn-x", pkts: make(chan *rtp.Packet, 2)}
	s.push(&rtp.Packet{})
	if pkt, err := s.ReadRTP(); err != nil || pkt == nil {
		t.Fatalf("read: pkt=%v err=%v", pkt, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s.push(&rtp.Packet{}) // must not panic (send on closed channel)
	if _, err := s.ReadRTP(); err == nil {
		t.Fatal("read after close should error")
	}
	_ = s.Close() // idempotent
}

func TestPstnSourceDropsWhenFull(t *testing.T) {
	s := &pstnSource{id: "pstn-x", pkts: make(chan *rtp.Packet, 1)}
	s.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}})
	s.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2}}) // buffer full → dropped, no stall
	pkt, err := s.ReadRTP()
	if err != nil || pkt.SequenceNumber != 1 {
		t.Fatalf("got seq=%d err=%v, want 1", pkt.SequenceNumber, err)
	}
}

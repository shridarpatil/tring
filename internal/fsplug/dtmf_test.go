package fsplug

import "testing"

// A telephone-event payload is: byte0=event, byte1=E|R|volume, byte2-3=duration.
func teEvent(event byte, end bool) []byte {
	b := []byte{event, 0x0a, 0x00, 0xa0}
	if end {
		b[1] |= 0x80
	}
	return b
}

func TestDTMFDecoderEmitsOncePerEvent(t *testing.T) {
	d := &dtmfDecoder{}
	const ts = uint32(1000)
	// Three repeat packets for digit '5' (event 5), same timestamp, no End bit → nothing yet.
	for i := 0; i < 3; i++ {
		if _, ok := d.push(ts, teEvent(5, false)); ok {
			t.Fatalf("emitted before End bit")
		}
	}
	// Final packet with End bit, same timestamp → emit once.
	digit, ok := d.push(ts, teEvent(5, true))
	if !ok || digit != "5" {
		t.Fatalf("got (%q,%v), want (\"5\", true)", digit, ok)
	}
	// A duplicate End packet for the same event (same timestamp) must NOT re-emit.
	if _, ok := d.push(ts, teEvent(5, true)); ok {
		t.Fatalf("re-emitted on duplicate End packet")
	}
}

// TestDTMFDecoderConsecutiveSameDigit is the regression test for the finding:
// two consecutive presses of the SAME digit are two distinct RFC 4733 events,
// each with its own RTP timestamp, and both must be emitted.
func TestDTMFDecoderConsecutiveSameDigit(t *testing.T) {
	d := &dtmfDecoder{}
	// First "5" event completes at timestamp 1000.
	digit, ok := d.push(1000, teEvent(5, true))
	if !ok || digit != "5" {
		t.Fatalf("first event: got (%q,%v), want (\"5\", true)", digit, ok)
	}
	// A fresh "5" event at a new timestamp (2000) must also emit.
	digit, ok = d.push(2000, teEvent(5, true))
	if !ok || digit != "5" {
		t.Fatalf("second event: got (%q,%v), want (\"5\", true)", digit, ok)
	}
}

func TestDTMFDecoderSymbols(t *testing.T) {
	for event := byte(0); event < byte(len(dtmfSymbols)); event++ {
		want := dtmfSymbols[event]
		d := &dtmfDecoder{}
		if digit, ok := d.push(uint32(event)+1, teEvent(event, true)); !ok || digit != want {
			t.Fatalf("event %d: got (%q,%v), want %q", event, digit, ok, want)
		}
	}
}

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
	// Three repeat packets for digit '5' (event 5), no End bit → nothing yet.
	for i := 0; i < 3; i++ {
		if _, ok := d.push(teEvent(5, false)); ok {
			t.Fatalf("emitted before End bit")
		}
	}
	// Final packet with End bit → emit once.
	digit, ok := d.push(teEvent(5, true))
	if !ok || digit != "5" {
		t.Fatalf("got (%q,%v), want (\"5\", true)", digit, ok)
	}
	// A duplicate End packet for the same event must NOT re-emit.
	if _, ok := d.push(teEvent(5, true)); ok {
		t.Fatalf("re-emitted on duplicate End packet")
	}
}

func TestDTMFDecoderSymbols(t *testing.T) {
	cases := map[byte]string{0: "0", 9: "9", 10: "*", 11: "#", 12: "A", 15: "D"}
	for event, want := range cases {
		d := &dtmfDecoder{}
		if digit, ok := d.push(teEvent(event, true)); !ok || digit != want {
			t.Fatalf("event %d: got (%q,%v), want %q", event, digit, ok, want)
		}
	}
}

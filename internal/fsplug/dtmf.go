// Package fsplug is the FreeSWITCH channel adapter (THE PLUG): a sipgo SIP UAS
// plus a plain-RTP endpoint that turns an inbound carrier call into a
// room.Leg. Nothing in internal/media imports this package.
package fsplug

// dtmfDecoder turns a stream of RFC 2833 telephone-event RTP payloads into
// digit strings, emitting each digit exactly once (on the event's End bit).
//
// Event identity is keyed on the RTP timestamp, not the event code: per RFC
// 4733, every packet belonging to one telephone-event carries the same RTP
// timestamp (the event's start time), while a fresh keypress — even of the
// same digit — starts a new event with a new timestamp. Keying on the code
// alone would silently drop a second consecutive press of the same digit
// (e.g. "55", PIN "1122").
type dtmfDecoder struct {
	haveTS  bool
	lastTS  uint32
	emitted bool
}

var dtmfSymbols = []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "#", "A", "B", "C", "D"}

// push feeds one telephone-event payload and its RTP timestamp. It returns
// (digit, true) once, when a new event completes (End bit set); otherwise
// ("", false).
func (d *dtmfDecoder) push(timestamp uint32, payload []byte) (string, bool) {
	if len(payload) < 2 {
		return "", false
	}
	event := payload[0]
	end := payload[1]&0x80 != 0

	// New event starting: reset emission flag so we emit exactly once.
	if !d.haveTS || timestamp != d.lastTS {
		d.haveTS = true
		d.lastTS = timestamp
		d.emitted = false
	}
	if !end {
		return "", false
	}
	// Emit once per completed event, never twice.
	if d.emitted {
		return "", false
	}
	d.emitted = true
	if int(event) >= len(dtmfSymbols) {
		return "", false
	}
	return dtmfSymbols[event], true
}

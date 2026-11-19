package room

// Codec is the audio codec a room carries end to end: every leg, browser mic,
// prompt and recording in the room uses it, because the media plane forwards
// RTP payloads without decoding them. The zero value is Opus (web and WhatsApp
// calls). A phone room carries the carrier's G.711 law instead, so FreeSWITCH
// relays the carrier's frames rather than transcoding every call to Opus.
type Codec string

const (
	Opus Codec = ""
	PCMA Codec = "PCMA" // G.711 A-law, static RTP payload type 8
	PCMU Codec = "PCMU" // G.711 µ-law, static RTP payload type 0
)

// G711 reports whether c is one of the G.711 laws (8 kHz, one byte a sample).
func (c Codec) G711() bool { return c == PCMA || c == PCMU }

// FrameSamples is the RTP timestamp step of one 20 ms frame: 960 on Opus's
// fixed 48 kHz RTP clock, 160 on G.711's 8 kHz one.
func (c Codec) FrameSamples() uint32 {
	if c.G711() {
		return 160
	}
	return 960
}

// PayloadType is the RTP payload type the media plane sends c with: G.711's
// static types, and 111 for Opus (the SFU's registration).
func (c Codec) PayloadType() uint8 {
	switch c {
	case PCMA:
		return 8
	case PCMU:
		return 0
	}
	return 111
}

// CodecForPayloadType maps a received packet's payload type back to its codec:
// the G.711 static types, anything else Opus (the only dynamic audio codec the
// media plane negotiates).
func CodecForPayloadType(pt uint8) Codec {
	switch pt {
	case 8:
		return PCMA
	case 0:
		return PCMU
	}
	return Opus
}

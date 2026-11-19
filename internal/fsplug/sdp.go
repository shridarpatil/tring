package fsplug

import (
	"fmt"
	"strings"

	"github.com/pion/sdp/v3"

	"github.com/shridarpatil/tring/internal/media/room"
)

// parseOffer extracts the audio media IP + port from a FreeSWITCH SDP offer,
// and the codec to answer with (see offeredCodec).
func parseOffer(body []byte) (string, int, room.Codec, error) {
	var desc sdp.SessionDescription
	if err := desc.Unmarshal(body); err != nil {
		return "", 0, room.Opus, fmt.Errorf("fsplug: bad SDP offer: %w", err)
	}
	ip := ""
	if desc.ConnectionInformation != nil && desc.ConnectionInformation.Address != nil {
		ip = desc.ConnectionInformation.Address.Address
	}
	for _, md := range desc.MediaDescriptions {
		if md.MediaName.Media != "audio" {
			continue
		}
		if md.ConnectionInformation != nil && md.ConnectionInformation.Address != nil {
			ip = md.ConnectionInformation.Address.Address // media-level c= wins
		}
		if ip == "" {
			return "", 0, room.Opus, fmt.Errorf("fsplug: no connection address for audio media")
		}
		return ip, md.MediaName.Port.Value, offeredCodec(md), nil
	}
	return "", 0, room.Opus, fmt.Errorf("fsplug: no audio media in offer")
}

// offeredCodec is the first of the plug's codecs (PCMA, PCMU, Opus) in the
// offer's preference order. FreeSWITCH's public dialplan offers the carrier's
// G.711 law alone, so the plug answers it and FS relays the carrier's frames
// untouched; anything else (WhatsApp) is answered with Opus, full band.
func offeredCodec(md *sdp.MediaDescription) room.Codec {
	names := map[string]string{"0": "PCMU", "8": "PCMA"} // static types may omit rtpmap
	for _, a := range md.Attributes {
		if a.Key != "rtpmap" {
			continue
		}
		pt, enc, _ := strings.Cut(a.Value, " ")
		name, _, _ := strings.Cut(enc, "/")
		names[pt] = strings.ToUpper(name)
	}
	for _, f := range md.MediaName.Formats {
		switch names[f] {
		case "PCMA":
			return room.PCMA
		case "PCMU":
			return room.PCMU
		case "OPUS":
			return room.Opus
		}
	}
	return room.Opus
}

// buildAnswer produces the plug's SDP (the answer, and the offer via
// buildOffer): one audio codec plus telephone-event, so FreeSWITCH negotiates
// exactly the codec the room carries.
//
// G.711 (c) goes out on its static payload type with an 8 kHz telephone-event:
// RFC 4733 events run on the audio codec's clock.
//
// For Opus (an inbound WhatsApp call, or a web room dialing out), rate, when
// set, is the carrier leg's audio rate (8000 on outbound calls, which reach the
// carrier as G.711): it goes out as maxplaybackrate/sprop-maxcapturerate, and
// FreeSWITCH then runs this Opus leg at that rate instead of 48 kHz — no
// resampling, and a far cheaper encoder (about a third of the CPU per call at
// 8 kHz). The RTP clock stays 48 kHz, as RFC 7587 fixes it for Opus whatever
// the audio bandwidth.
func buildAnswer(localIP string, localPort int, c room.Codec, opusPT, dtmfPT uint8, rate int) []byte {
	pt, clock := opusPT, 48000
	codecLines := fmt.Sprintf("a=rtpmap:%d opus/48000/2\na=fmtp:%d minptime=10;useinbandfec=1;stereo=0;sprop-stereo=0", pt, pt)
	if rate > 0 {
		codecLines += fmt.Sprintf(";maxplaybackrate=%d;sprop-maxcapturerate=%d", rate, rate)
	}
	if c.G711() {
		pt, clock = c.PayloadType(), 8000
		codecLines = fmt.Sprintf("a=rtpmap:%d %s/8000", pt, c)
	}
	return []byte(fmt.Sprintf(`v=0
o=tring 0 0 IN IP4 %s
s=tring
c=IN IP4 %s
t=0 0
m=audio %d RTP/AVP %d %d
%s
a=rtpmap:%d telephone-event/%d
a=fmtp:%d 0-16
a=ptime:20
a=sendrecv
`, localIP, localIP, localPort, pt, dtmfPT, codecLines, dtmfPT, clock, dtmfPT))
}

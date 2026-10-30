package fsplug

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/emiago/sipgo/sip"
	"github.com/pion/sdp/v3"
)

// parseOffer extracts the audio media IP + port from a FreeSWITCH SDP offer.
func parseOffer(body []byte) (string, int, error) {
	var desc sdp.SessionDescription
	if err := desc.Unmarshal(body); err != nil {
		return "", 0, fmt.Errorf("fsplug: bad SDP offer: %w", err)
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
			return "", 0, fmt.Errorf("fsplug: no connection address for audio media")
		}
		return ip, md.MediaName.Port.Value, nil
	}
	return "", 0, fmt.Errorf("fsplug: no audio media in offer")
}

// buildAnswer produces an Opus-only (+ telephone-event) SDP answer. Opus-only
// forces FreeSWITCH to transcode the G.711 carrier leg, so the plug only ever
// sees Opus.
//
// rate, when set, is the carrier leg's audio rate (8000 for G.711): it goes out
// as maxplaybackrate/sprop-maxcapturerate, and FreeSWITCH then runs this Opus
// leg at that rate instead of 48 kHz — no resampling, and a far cheaper
// encoder (about a third of the CPU per call at 8 kHz). Full band would carry
// nothing a phone line has. The RTP clock stays 48 kHz, as Opus requires.
func buildAnswer(localIP string, localPort int, opusPT, dtmfPT uint8, rate int) []byte {
	fmtp := "minptime=10;useinbandfec=1;stereo=0;sprop-stereo=0"
	if rate > 0 {
		fmtp += fmt.Sprintf(";maxplaybackrate=%d;sprop-maxcapturerate=%d", rate, rate)
	}
	return []byte(fmt.Sprintf(`v=0
o=tring 0 0 IN IP4 %s
s=tring
c=IN IP4 %s
t=0 0
m=audio %d RTP/AVP %d %d
a=rtpmap:%d opus/48000/2
a=fmtp:%d %s
a=rtpmap:%d telephone-event/48000
a=fmtp:%d 0-16
a=ptime:20
a=sendrecv
`, localIP, localIP, localPort, opusPT, dtmfPT, opusPT, opusPT, fmtp, dtmfPT, dtmfPT))
}

// audioRates are the Opus rates a carrier leg's X-Tring-Audio-Rate may name.
var audioRates = map[int]bool{8000: true, 12000: true, 16000: true, 24000: true}

// audioRate reads the carrier leg's audio rate from the X-Tring-Audio-Rate
// header FreeSWITCH's public dialplan sets on the bridge (0 when absent or not
// an Opus rate, e.g. a WhatsApp call: full band, as WhatsApp audio is).
func audioRate(req *sip.Request) int {
	h := req.GetHeader("X-Tring-Audio-Rate")
	if h == nil {
		return 0
	}
	if r, err := strconv.Atoi(strings.TrimSpace(h.Value())); err == nil && audioRates[r] {
		return r
	}
	return 0
}

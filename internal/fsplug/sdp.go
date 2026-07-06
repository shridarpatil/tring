package fsplug

import (
	"fmt"

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
		return ip, md.MediaName.Port.Value, nil
	}
	return "", 0, fmt.Errorf("fsplug: no audio media in offer")
}

// buildAnswer produces an Opus-only (+ telephone-event) SDP answer. Opus-only
// forces FreeSWITCH to transcode the G.711 carrier leg, so the plug only ever
// sees Opus.
func buildAnswer(localIP string, localPort int, opusPT, dtmfPT uint8) []byte {
	return []byte(fmt.Sprintf(`v=0
o=tring 0 0 IN IP4 %s
s=tring
c=IN IP4 %s
t=0 0
m=audio %d RTP/AVP %d %d
a=rtpmap:%d opus/48000/2
a=fmtp:%d minptime=10;useinbandfec=1;stereo=0;sprop-stereo=0
a=rtpmap:%d telephone-event/48000
a=fmtp:%d 0-16
a=ptime:20
a=sendrecv
`, localIP, localIP, localPort, opusPT, dtmfPT, opusPT, opusPT, dtmfPT, dtmfPT))
}

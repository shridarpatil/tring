package fsplug

import (
	"strings"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/pion/sdp/v3"
)

const fsOffer = `v=0
o=FreeSWITCH 1 1 IN IP4 127.0.0.1
s=FreeSWITCH
c=IN IP4 127.0.0.1
t=0 0
m=audio 30000 RTP/AVP 111 101
a=rtpmap:111 opus/48000/2
a=rtpmap:101 telephone-event/48000
a=ptime:20
`

func TestParseOffer(t *testing.T) {
	ip, port, err := parseOffer([]byte(fsOffer))
	if err != nil {
		t.Fatal(err)
	}
	if ip != "127.0.0.1" || port != 30000 {
		t.Fatalf("got %s:%d, want 127.0.0.1:30000", ip, port)
	}
}

func TestBuildAnswerIsOpusOnly(t *testing.T) {
	ans := string(buildAnswer("127.0.0.1", 12000, 111, 101, 0))
	for _, want := range []string{"m=audio 12000 RTP/AVP 111 101", "opus/48000/2", "telephone-event/48000", "a=ptime:20"} {
		if !strings.Contains(ans, want) {
			t.Fatalf("answer missing %q:\n%s", want, ans)
		}
	}
	if strings.Contains(ans, "PCMU") || strings.Contains(ans, "PCMA") {
		t.Fatalf("answer offered a G.711 codec:\n%s", ans)
	}
}

func TestBuildAnswerIsWellFormed(t *testing.T) {
	ans := buildAnswer("127.0.0.1", 12000, 111, 101, 0)
	var desc sdp.SessionDescription
	if err := desc.Unmarshal(ans); err != nil {
		t.Fatalf("produced answer failed to parse: %v\n%s", err, ans)
	}
	var audio *sdp.MediaDescription
	for _, md := range desc.MediaDescriptions {
		if md.MediaName.Media == "audio" {
			audio = md
			break
		}
	}
	if audio == nil {
		t.Fatalf("no audio media description in answer:\n%s", ans)
	}
	if len(audio.MediaName.Formats) != 2 {
		t.Fatalf("expected exactly 2 payload types, got %v", audio.MediaName.Formats)
	}
	if audio.MediaName.Formats[0] != "111" || audio.MediaName.Formats[1] != "101" {
		t.Fatalf("unexpected payload types: %v", audio.MediaName.Formats)
	}
}

const fsOfferMediaLevelConn = `v=0
o=FreeSWITCH 1 1 IN IP4 127.0.0.1
s=FreeSWITCH
c=IN IP4 10.0.0.1
t=0 0
m=audio 30000 RTP/AVP 111 101
c=IN IP4 192.168.1.50
a=rtpmap:111 opus/48000/2
a=rtpmap:101 telephone-event/48000
a=ptime:20
`

func TestParseOfferMediaLevelConnWins(t *testing.T) {
	ip, port, err := parseOffer([]byte(fsOfferMediaLevelConn))
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.1.50" || port != 30000 {
		t.Fatalf("got %s:%d, want 192.168.1.50:30000", ip, port)
	}
}

const fsOfferNoAudio = `v=0
o=FreeSWITCH 1 1 IN IP4 127.0.0.1
s=FreeSWITCH
c=IN IP4 127.0.0.1
t=0 0
m=video 30002 RTP/AVP 96
a=rtpmap:96 VP8/90000
`

func TestParseOfferNoAudio(t *testing.T) {
	_, _, err := parseOffer([]byte(fsOfferNoAudio))
	if err == nil {
		t.Fatal("expected error for offer with no audio media, got nil")
	}
}

const fsOfferNoConnection = `v=0
o=FreeSWITCH 1 1 IN IP4 127.0.0.1
s=FreeSWITCH
t=0 0
m=audio 30000 RTP/AVP 111 101
a=rtpmap:111 opus/48000/2
a=rtpmap:101 telephone-event/48000
a=ptime:20
`

func TestParseOfferNoConnection(t *testing.T) {
	_, _, err := parseOffer([]byte(fsOfferNoConnection))
	if err == nil {
		t.Fatal("expected error for offer with no connection address, got nil")
	}
}

func TestParseOfferMalformed(t *testing.T) {
	if _, _, err := parseOffer([]byte("not sdp at all")); err == nil {
		t.Fatal("expected error for malformed SDP, got nil")
	}
	if _, _, err := parseOffer(nil); err == nil {
		t.Fatal("expected error for nil SDP, got nil")
	}
}

func TestBuildAnswerCapsOpusAtCarrierRate(t *testing.T) {
	if ans := string(buildAnswer("127.0.0.1", 12000, 111, 101, 8000)); !strings.Contains(ans,
		"a=fmtp:111 minptime=10;useinbandfec=1;stereo=0;sprop-stereo=0;maxplaybackrate=8000;sprop-maxcapturerate=8000\n") {
		t.Errorf("8 kHz answer doesn't cap Opus:\n%s", ans)
	}
	if ans := string(buildAnswer("127.0.0.1", 12000, 111, 101, 0)); strings.Contains(ans, "maxplaybackrate") {
		t.Errorf("full-band answer caps Opus:\n%s", ans)
	}
}

func TestAudioRate(t *testing.T) {
	for in, want := range map[string]int{"8000": 8000, " 16000 ": 16000, "48000": 0, "": 0, "8k": 0, "-8000": 0} {
		req := sip.NewRequest(sip.INVITE, sip.Uri{User: "919000000001", Host: "127.0.0.1"})
		req.AppendHeader(sip.NewHeader("X-Tring-Audio-Rate", in))
		if got := audioRate(req); got != want {
			t.Errorf("X-Tring-Audio-Rate %q → %d, want %d", in, got, want)
		}
	}
	if got := audioRate(sip.NewRequest(sip.INVITE, sip.Uri{User: "919000000001", Host: "127.0.0.1"})); got != 0 {
		t.Errorf("no header → %d, want 0", got)
	}
}

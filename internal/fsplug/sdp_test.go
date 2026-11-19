package fsplug

import (
	"strings"
	"testing"

	"github.com/pion/sdp/v3"

	"github.com/shridarpatil/tring/internal/media/room"
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
	ip, port, _, err := parseOffer([]byte(fsOffer))
	if err != nil {
		t.Fatal(err)
	}
	if ip != "127.0.0.1" || port != 30000 {
		t.Fatalf("got %s:%d, want 127.0.0.1:30000", ip, port)
	}
}

func TestBuildAnswerIsOpusOnly(t *testing.T) {
	ans := string(buildAnswer("127.0.0.1", 12000, room.Opus, 111, 101, 0))
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
	ans := buildAnswer("127.0.0.1", 12000, room.Opus, 111, 101, 0)
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
	ip, port, _, err := parseOffer([]byte(fsOfferMediaLevelConn))
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
	_, _, _, err := parseOffer([]byte(fsOfferNoAudio))
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
	_, _, _, err := parseOffer([]byte(fsOfferNoConnection))
	if err == nil {
		t.Fatal("expected error for offer with no connection address, got nil")
	}
}

func TestParseOfferMalformed(t *testing.T) {
	if _, _, _, err := parseOffer([]byte("not sdp at all")); err == nil {
		t.Fatal("expected error for malformed SDP, got nil")
	}
	if _, _, _, err := parseOffer(nil); err == nil {
		t.Fatal("expected error for nil SDP, got nil")
	}
}

func TestBuildAnswerCapsOpusAtCarrierRate(t *testing.T) {
	if ans := string(buildAnswer("127.0.0.1", 12000, room.Opus, 111, 101, 8000)); !strings.Contains(ans,
		"a=fmtp:111 minptime=10;useinbandfec=1;stereo=0;sprop-stereo=0;maxplaybackrate=8000;sprop-maxcapturerate=8000\n") {
		t.Errorf("8 kHz answer doesn't cap Opus:\n%s", ans)
	}
	if ans := string(buildAnswer("127.0.0.1", 12000, room.Opus, 111, 101, 0)); strings.Contains(ans, "maxplaybackrate") {
		t.Errorf("full-band answer caps Opus:\n%s", ans)
	}
}

// A G.711 answer carries the law alone on its static payload type, with an
// 8 kHz telephone-event so DTMF keeps working on the law's clock.
func TestBuildAnswerG711(t *testing.T) {
	for _, tc := range []struct {
		c    room.Codec
		want []string
	}{
		{room.PCMA, []string{"m=audio 12000 RTP/AVP 8 101", "a=rtpmap:8 PCMA/8000"}},
		{room.PCMU, []string{"m=audio 12000 RTP/AVP 0 101", "a=rtpmap:0 PCMU/8000"}},
	} {
		ans := string(buildAnswer("127.0.0.1", 12000, tc.c, 111, 101, 8000))
		for _, want := range append(tc.want, "a=rtpmap:101 telephone-event/8000", "a=fmtp:101 0-16", "a=ptime:20") {
			if !strings.Contains(ans, want) {
				t.Fatalf("%s answer missing %q:\n%s", tc.c, want, ans)
			}
		}
		if strings.Contains(strings.ToLower(ans), "opus") || strings.Contains(ans, "/48000") {
			t.Fatalf("%s answer still carries Opus/48k:\n%s", tc.c, ans)
		}
		var desc sdp.SessionDescription
		if err := desc.Unmarshal([]byte(ans)); err != nil {
			t.Fatalf("%s answer does not parse: %v", tc.c, err)
		}
	}
}

// The plug answers whichever of its codecs FreeSWITCH offers first.
func TestParseOfferCodec(t *testing.T) {
	offer := func(formats string, maps ...string) []byte {
		return []byte("v=0\r\no=FreeSWITCH 1 1 IN IP4 127.0.0.1\r\ns=FreeSWITCH\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
			"m=audio 30000 RTP/AVP " + formats + "\r\n" + strings.Join(maps, "") + "a=ptime:20\r\n")
	}
	for name, tc := range map[string]struct {
		body []byte
		want room.Codec
	}{
		"pcma":                   {offer("8 101", "a=rtpmap:8 PCMA/8000\r\n", "a=rtpmap:101 telephone-event/8000\r\n"), room.PCMA},
		"pcmu static, no rtpmap": {offer("0 101", "a=rtpmap:101 telephone-event/8000\r\n"), room.PCMU},
		"opus first":             {offer("102 8 101", "a=rtpmap:102 opus/48000/2\r\n", "a=rtpmap:8 PCMA/8000\r\n"), room.Opus},
		"pcma before opus":       {offer("8 102", "a=rtpmap:102 opus/48000/2\r\n"), room.PCMA},
		"nothing known":          {offer("9 101", "a=rtpmap:9 G722/8000\r\n"), room.Opus},
	} {
		_, _, got, err := parseOffer(tc.body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: codec = %q, want %q", name, got, tc.want)
		}
	}
}

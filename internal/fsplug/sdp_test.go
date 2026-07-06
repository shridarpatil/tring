package fsplug

import (
	"strings"
	"testing"
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
	ans := string(buildAnswer("127.0.0.1", 12000, 111, 101))
	for _, want := range []string{"m=audio 12000 RTP/AVP 111 101", "opus/48000/2", "telephone-event/48000", "a=ptime:20"} {
		if !strings.Contains(ans, want) {
			t.Fatalf("answer missing %q:\n%s", want, ans)
		}
	}
	if strings.Contains(ans, "PCMU") || strings.Contains(ans, "PCMA") {
		t.Fatalf("answer offered a G.711 codec:\n%s", ans)
	}
}

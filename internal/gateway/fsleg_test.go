package gateway

import "testing"

// As a UAS answering FreeSWITCH, the gateway MUST echo the payload types FS
// offered (a UAS can't renumber the offerer's PTs). buildSDP + parseSDPMedia
// must round-trip those PTs.
func TestBuildSDPEchoesOfferedPTs(t *testing.T) {
	// FS offered Opus as 96 and telephone-event as 100 — not our defaults.
	answer := buildSDP("10.0.0.5", 40000, 96, 100)

	addr, opusPT, dtmfPT, err := parseSDPMedia(answer)
	if err != nil {
		t.Fatalf("parse our own answer: %v", err)
	}
	if addr.IP.String() != "10.0.0.5" || addr.Port != 40000 {
		t.Fatalf("addr = %s, want 10.0.0.5:40000", addr)
	}
	if opusPT != 96 {
		t.Fatalf("opus PT = %d, want 96 (echoed)", opusPT)
	}
	if dtmfPT != 100 {
		t.Fatalf("dtmf PT = %d, want 100 (echoed)", dtmfPT)
	}
}

func TestParseSDPMediaDefaultsWhenCodecsMissing(t *testing.T) {
	// An m-line with only the ports, no rtpmap → fall back to 111/101.
	sdp := "v=0\r\nc=IN IP4 1.2.3.4\r\nm=audio 5000 RTP/AVP 0\r\n"
	addr, opusPT, dtmfPT, err := parseSDPMedia(sdp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if addr.Port != 5000 {
		t.Fatalf("port = %d, want 5000", addr.Port)
	}
	if opusPT != 111 || dtmfPT != 101 {
		t.Fatalf("PT fallbacks = %d/%d, want 111/101", opusPT, dtmfPT)
	}
}

func TestParseSDPMediaRejectsNoAudio(t *testing.T) {
	if _, _, _, err := parseSDPMedia("v=0\r\nc=IN IP4 1.2.3.4\r\n"); err == nil {
		t.Fatal("expected error when no audio media line present")
	}
}

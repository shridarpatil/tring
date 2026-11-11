package record_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/shridarpatil/tring/internal/media/record"
)

func videoPacket(ts uint32, keyframe bool) *rtp.Packet {
	frame := byte(1)
	if keyframe {
		frame = 0
	}
	return &rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: ts, Marker: true}, Payload: []byte{0x10, frame, 0, 0, 0}}
}

func TestVideoCaptureAnswerGateAndRestart(t *testing.T) {
	rec := record.New(t.TempDir(), "call")
	rec.Label("a", "agent")
	write := func(stream string, ts uint32, key bool) {
		t.Helper()
		pkt := videoPacket(ts, key)
		if err := rec.WriteVideoRTP("a", stream, "video/VP8", pkt); err != nil {
			t.Fatal(err)
		}
		if pkt.Timestamp != ts {
			t.Fatal("recorder changed the forwarded RTP timestamp")
		}
	}
	write("first", 3000, true)
	if len(rec.VideoTracks()) != 0 {
		t.Fatal("recorded before answer")
	}
	rec.Arm()
	write("first", 6000, false)
	if len(rec.VideoTracks()) != 0 {
		t.Fatal("recording began without a keyframe")
	}
	write("first", 9000, true)
	time.Sleep(15 * time.Millisecond)
	write("first", 12000, false)
	rec.EndVideo("a", "first")
	before := rec.VideoTracks()[0]
	data, err := os.ReadFile(before.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "DKIF" || binary.LittleEndian.Uint32(data[24:]) != 2 {
		t.Fatal("invalid IVF frame capture")
	}
	write("first", 15000, true) // late packets cannot reopen an ended stream
	time.Sleep(10 * time.Millisecond)
	write("second", 0, true) // reconnect resets sender clock
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	tracks := rec.VideoTracks()
	if len(tracks) != 2 || tracks[0].Role != "agent" || tracks[1].Offset <= tracks[0].Offset+tracks[0].Duration {
		t.Fatalf("bad intervals: %+v", tracks)
	}
	write("third", 0, true)
	if len(rec.VideoTracks()) != 2 {
		t.Fatal("recorded after hangup")
	}
	if rec.Duration() < tracks[1].Offset {
		t.Fatal("call duration truncated")
	}
}

func TestComposeVideoIncludesBothPeopleAndCameraOffGaps(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	red, blue := filepath.Join(dir, "agent.ivf"), filepath.Join(dir, "client.ivf")
	for _, v := range []struct{ path, color string }{{red, "red"}, {blue, "blue"}} {
		b, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c="+v.color+":s=160x120:r=24:d=1", "-c:v", "libvpx", "-threads", "1", v.path).CombinedOutput()
		if err != nil {
			t.Fatalf("generate video: %v: %s", err, b)
		}
	}
	agent, client := filepath.Join(dir, "a.ogg"), filepath.Join(dir, "c.ogg")
	genToneDur(t, agent, 300, 3)
	genToneDur(t, client, 1200, 3)
	out := filepath.Join(dir, "call.mp4")
	err := record.ComposeVideo(context.Background(), out, []record.Track{{Role: "agent", Path: agent}, {Role: "customer", Path: client}}, []record.VideoTrack{
		{Role: "agent", Path: red, Offset: 500 * time.Millisecond, Duration: time.Second},
		{Role: "customer", Path: blue, Offset: time.Second, Duration: 1500 * time.Millisecond},
	}, 3*time.Second, 48000)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the center pixel of each panel at four points on the call timeline.
	for _, tc := range []struct {
		at        float64
		red, blue bool
	}{{0.2, false, false}, {0.8, true, false}, {1.2, true, true}, {2, false, true}, {2.8, false, false}} {
		raw, err := exec.Command("ffmpeg", "-v", "error", "-ss", fmt.Sprintf("%.1f", tc.at), "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
		if err != nil || len(raw) != 1280*480*3 {
			t.Fatalf("decode at %v: %v (%d bytes)", tc.at, err, len(raw))
		}
		l, r := raw[(240*1280+320)*3:], raw[(240*1280+960)*3:]
		if got := int(l[0]) > int(l[1])+100; got != tc.red {
			t.Errorf("agent at %.1f red=%v, want %v", tc.at, got, tc.red)
		}
		if got := int(r[2]) > int(r[0])+100; got != tc.blue {
			t.Errorf("client at %.1f blue=%v, want %v", tc.at, got, tc.blue)
		}
	}
	left, right := decodeStereo(t, out)
	if len(left) < 3*48000 || rms(left) < 1000 || rms(right) < 1000 || meanAbsDiff(left, right) < 1000 {
		t.Fatal("both participants' distinct audio must span the recording")
	}
}

func TestCapturedVideoDecodesFromFragmentedRTP(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ivf, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=160x120:r=24", "-frames:v", "1", "-c:v", "libvpx", "-threads", "1", "-f", "ivf", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	size := int(binary.LittleEndian.Uint32(ivf[32:]))
	frame := ivf[44 : 44+size]
	rec := record.New(t.TempDir(), "fragmented")
	rec.Label("agent", "agent")
	rec.Arm()
	payloader := &codecs.VP8Payloader{}
	var seq uint16
	for n := range 2 {
		if n > 0 {
			time.Sleep(45 * time.Millisecond)
		}
		payloads := payloader.Payload(40, frame)
		for i, payload := range payloads {
			seq++
			pkt := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(n * 4000), Marker: i == len(payloads)-1}, Payload: payload}
			if err := rec.WriteVideoRTP("agent", "camera", "video/VP8", pkt); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Malformed subsequent packets cannot crash an active capture.
	if err := rec.WriteVideoRTP("agent", "camera", "video/VP8", &rtp.Packet{Payload: []byte{0x10}}); err == nil {
		t.Fatal("accepted empty frame")
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	tracks := rec.VideoTracks()
	if len(tracks) != 1 {
		t.Fatal("missing capture")
	}
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", tracks[0].Path, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil || len(raw) != 160*120*3*2 {
		t.Fatalf("capture did not decode both RTP frames: %v (%d bytes)", err, len(raw))
	}
	if raw[0] < 200 || raw[1] > 30 || raw[2] > 30 {
		t.Fatal("captured frame color changed")
	}
}

// Replacing a camera with a screen can change resolution within one RTP stream.
// Filter reinitialization must not reset the composite's call-relative timeline.
func TestComposeVideoResolutionChangePreservesTimeline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	var capture []byte
	for i, spec := range []string{"color=c=red:s=160x120:r=24:d=1", "color=c=blue:s=320x180:r=24:d=1"} {
		ivf, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", spec, "-c:v", "libvpx", "-threads", "1", "-f", "ivf", "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			capture = append(capture, ivf[:32]...)
		}
		for pos := 32; pos < len(ivf); {
			size := int(binary.LittleEndian.Uint32(ivf[pos:]))
			pts := binary.LittleEndian.Uint64(ivf[pos+4:]) + uint64(i*24)
			binary.LittleEndian.PutUint64(ivf[pos+4:], pts)
			capture = append(capture, ivf[pos:pos+12+size]...)
			pos += 12 + size
		}
	}
	binary.LittleEndian.PutUint32(capture[24:], 48)
	dir := t.TempDir()
	input, out := filepath.Join(dir, "switch.ivf"), filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(input, capture, 0600); err != nil {
		t.Fatal(err)
	}
	if err := record.ComposeVideo(context.Background(), out, nil, []record.VideoTrack{{Role: "agent", Path: input, Duration: 2 * time.Second}}, 2500*time.Millisecond, 8000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at   string
		blue bool
	}{{"0.5", false}, {"1.5", true}} {
		raw, err := exec.Command("ffmpeg", "-v", "error", "-ss", tc.at, "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
		if err != nil || len(raw) != 1280*480*3 {
			t.Fatalf("decode: %v, bytes=%d", err, len(raw))
		}
		pixel := raw[(240*1280+320)*3:]
		blue := int(pixel[2]) > int(pixel[0])+100
		if blue != tc.blue {
			t.Errorf("at %ss blue=%v, want %v (timeline reset)", tc.at, blue, tc.blue)
		}
	}
}

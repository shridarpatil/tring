package record

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"

	"github.com/shridarpatil/tring/internal/media/room"
)

// VideoTrack is one continuous camera/screen interval, on the recording's
// answer-relative timeline. Turning video off/on creates separate intervals.
type VideoTrack struct {
	PID              room.ParticipantID
	Role, Path       string
	Offset, Duration time.Duration
}

type videoCapture struct {
	pid               room.ParticipantID
	path              string
	writer            *ivfwriter.IVFWriter
	start, end        time.Time
	frames            int
	lastRTP, framePTS uint32
}

func videoKeyframe(codec string, pkt *rtp.Packet) bool {
	switch codec {
	case "video/VP8":
		p := codecs.VP8Packet{}
		_, err := p.Unmarshal(pkt.Payload)
		return err == nil && p.S == 1 && p.PID == 0 && len(p.Payload) > 0 && p.Payload[0]&1 == 0
	case "video/VP9":
		p := codecs.VP9Packet{}
		_, err := p.Unmarshal(pkt.Payload)
		return err == nil && p.B && !p.P
	}
	return false
}

func (r *Recorder) WriteVideoRTP(pid room.ParticipantID, stream, codec string, pkt *rtp.Packet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.armed || r.endedVideo[stream] {
		return nil
	}
	if codec != "video/VP8" && codec != "video/VP9" {
		return fmt.Errorf("unsupported video recording codec %q", codec)
	}
	// IVFWriter indexes VP8 Payload[0] after depacketization. An empty frame
	// must never reach it, including after a valid capture has already started.
	if codec == "video/VP8" {
		p := codecs.VP8Packet{}
		if _, err := p.Unmarshal(pkt.Payload); err != nil || len(p.Payload) == 0 {
			return fmt.Errorf("invalid VP8 recording packet")
		}
	} else {
		p := codecs.VP9Packet{}
		if _, err := p.Unmarshal(pkt.Payload); err != nil || len(p.Payload) == 0 {
			return fmt.Errorf("invalid VP9 recording packet")
		}
	}
	v := r.videos[stream]
	if v == nil {
		// Start at a keyframe; an already-running camera can join the recording
		// only once its picture can be decoded independently.
		if !videoKeyframe(codec, pkt) {
			return nil
		}
		if err := os.MkdirAll(r.dir, 0755); err != nil {
			return err
		}
		path := filepath.Join(r.dir, fmt.Sprintf("%s-%s-%s.ivf", r.callID, sanitize(string(pid)), uuid.NewString()))
		writer, err := ivfwriter.New(path, ivfwriter.WithCodec(codec), ivfwriter.WithFrameRate(1, 90000), ivfwriter.WithDirectPTS())
		if err != nil {
			return err
		}
		v = &videoCapture{pid: pid, path: path, writer: writer, start: time.Now(), lastRTP: pkt.Timestamp}
		r.videos[stream] = v
	}
	// Each frame uses its arrival time relative to the capture, keeping both
	// cameras aligned with audio even if sender RTP clocks restart on reconnect.
	if pkt.Timestamp != v.lastRTP {
		v.framePTS = uint32(time.Since(v.start).Nanoseconds() * 90000 / int64(time.Second))
		v.lastRTP = pkt.Timestamp
	}
	copy := *pkt
	copy.Timestamp = v.framePTS
	if err := v.writer.WriteRTP(&copy); err != nil {
		return err
	}
	if pkt.Marker {
		v.frames++
	}
	return nil
}

func (r *Recorder) EndVideo(_ room.ParticipantID, stream string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endedVideo[stream] = true
	if v := r.videos[stream]; v != nil && v.end.IsZero() {
		v.end = time.Now()
		_ = v.writer.Close()
	}
}

func (r *Recorder) VideoTracks() []VideoTrack {
	r.mu.Lock()
	defer r.mu.Unlock()
	var tracks []VideoTrack
	for _, v := range r.videos {
		if v.frames == 0 {
			continue
		}
		end := v.end
		if end.IsZero() {
			end = time.Now()
		}
		tracks = append(tracks, VideoTrack{PID: v.pid, Role: r.roleOf(v.pid), Path: v.path, Offset: v.start.Sub(r.armedAt), Duration: end.Sub(v.start)})
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Offset < tracks[j].Offset })
	return tracks
}

func (r *Recorder) Duration() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.armed {
		return 0
	}
	end := r.closedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(r.armedAt)
}

var _ room.VideoSink = (*Recorder)(nil)

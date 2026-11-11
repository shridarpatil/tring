package callcdr

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/shridarpatil/tring/internal/media/record"
)

// Bound video encoder CPU/memory; capture itself stays on the existing RTP path.
var videoFinalizers = make(chan struct{}, 2)

func (l *Log) finalizeVideoRecording(ac *activeCall, audio []record.Track, video []record.VideoTrack) {
	videoFinalizers <- struct{}{}
	defer func() { <-videoFinalizers }()
	// Video composition can take longer than the audio merge. Allow time in
	// proportion to call length, with a ceiling for broken inputs/encoders.
	timeout := 2*time.Minute + ac.rec.Duration()*2
	if timeout > 30*time.Minute {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out := filepath.Join(l.dir, ac.legID+".mp4")
	partial := filepath.Join(l.dir, ac.legID+".partial.mp4")
	defer os.Remove(partial)
	if err := record.ComposeVideo(ctx, partial, audio, video, ac.rec.Duration(), ac.recRate); err != nil {
		// Keep every source for recovery. Never advertise a customer-only fallback
		// as a successful recording of a two-party video call.
		l.log.Error("cdr: video composition failed; captures retained", "call", ac.id, "err", err)
		return
	}
	if err := os.Rename(partial, out); err != nil {
		l.log.Error("cdr: video finalize failed; captures retained", "call", ac.id, "err", err)
		return
	}
	for _, track := range audio {
		_ = os.Remove(track.Path)
	}
	for _, track := range video {
		_ = os.Remove(track.Path)
	}
	l.publishRecording(ac, out)
}

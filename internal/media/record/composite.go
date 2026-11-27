package record

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Rasterized from the adjacent SVG sources so recording hosts do not need
// installed fonts, librsvg, or FFmpeg's optional drawtext filter.
//
//go:embed placeholders/*.png
var videoPlaceholders embed.FS

// Each person's panel and the frame rate. Composing re-encodes the whole call,
// so these set its cost: 320x240 at 15 fps took ~60% less CPU than 640x480 at
// 24 fps in a two-camera benchmark, and files ~80% smaller, while faces in a
// call recording stay recognisable. The placeholder PNGs are this size too, so
// they need no per-frame scaling.
const (
	panelW   = 320
	panelH   = 240
	videoFPS = "15"
)

// ComposeVideo produces one MP4: agent on the left, client on the right, with
// both audio channels. A labelled avatar fills camera-off intervals. Every segment
// is positioned on the answer-relative timeline, including transfers/restarts.
func ComposeVideo(ctx context.Context, out string, audio []Track, video []VideoTrack, duration time.Duration, rate int) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return ErrFFmpegMissing
	}
	if duration <= 0 {
		return fmt.Errorf("video recording has no duration")
	}
	encoder, err := videoEncoder(ctx)
	if err != nil {
		return err
	}
	seconds := func(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 6, 64) }
	total := seconds(duration)
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-filter_complex_threads", "1"}
	placeholderDir, err := os.MkdirTemp(filepath.Dir(out), ".video-placeholders-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(placeholderDir)
	for _, role := range []string{"agent", "client"} {
		data, err := videoPlaceholders.ReadFile("placeholders/" + role + "-camera-off.png")
		if err != nil {
			return err
		}
		path := filepath.Join(placeholderDir, role+".png")
		if err := os.WriteFile(path, data, 0600); err != nil {
			return err
		}
		args = append(args, "-loop", "1", "-framerate", videoFPS, "-i", path)
	}
	var videos []VideoTrack
	var audios []Track
	for _, v := range video {
		if v.Role == "agent" || v.Role == "customer" {
			videos = append(videos, v)
			// A camera→screen switch changes resolution mid-stream. By default
			// FFmpeg rebuilds the whole filtergraph on that and discards the
			// frames buffered in it (~0.5s of video vanishes before the switch).
			// scale and pad re-fit every frame (eval=frame), so everything after
			// them sees a fixed panel size and the rebuild is never needed.
			args = append(args, "-reinit_filter", "0", "-i", v.Path)
		}
	}
	for _, a := range audio {
		if a.Role == "agent" || a.Role == "customer" {
			audios = append(audios, a)
			args = append(args, "-i", a.Path)
		}
	}
	var fc strings.Builder
	fmt.Fprintf(&fc, "[0:v]trim=duration=%s,setpts=PTS-STARTPTS[left0];[1:v]trim=duration=%s,setpts=PTS-STARTPTS[right0];", total, total)
	left, right := "left0", "right0"
	for i, v := range videos {
		if v.Duration <= 0 {
			continue
		}
		// tpad holds the final picture only until camera-off, never through the
		// rest of the call. Delayed starts expose the placeholder beneath it.
		fmt.Fprintf(&fc, "[%d:v]setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease:eval=frame,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:eval=frame,setsar=1,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS+%s/TB[v%d];", i+2, panelW, panelH, panelW, panelH, seconds(v.Duration), seconds(v.Duration), seconds(v.Offset), i)
		base := right
		if v.Role == "agent" {
			base = left
		}
		next := fmt.Sprintf("panel%d", i)
		fmt.Fprintf(&fc, "[%s][v%d]overlay=eof_action=pass:repeatlast=0:enable='between(t,%s,%s)'[%s];", base, i, seconds(v.Offset), seconds(v.Offset+v.Duration), next)
		if v.Role == "agent" {
			left = next
		} else {
			right = next
		}
	}
	fmt.Fprintf(&fc, "[%s][%s]hstack=inputs=2,format=yuv420p[v];", left, right)
	fmt.Fprintf(&fc, "anullsrc=r=%d:cl=stereo,atrim=duration=%s[silence];", rate, total)
	labels := "[silence]"
	for i, a := range audios {
		pan := "c0=c0|c1=0*c0"
		if a.Role == "customer" {
			pan = "c0=0*c0|c1=c0"
		}
		fmt.Fprintf(&fc, "[%d:a]pan=stereo|%s,adelay=%d|%d[a%d];", 2+len(videos)+i, pan, a.Offset.Milliseconds(), a.Offset.Milliseconds(), i)
		labels += fmt.Sprintf("[a%d]", i)
	}
	fmt.Fprintf(&fc, "%samix=inputs=%d:duration=first:normalize=0[a]", labels, len(audios)+1)
	args = append(args, "-filter_complex", fc.String(), "-map", "[v]", "-map", "[a]")
	args = append(args, encoder...)
	args = append(args, "-threads", "2", "-c:a", "aac", "-ar", strconv.Itoa(rate), "-b:a", "128k", "-movflags", "+faststart", "-t", total, out)
	if b, err := runLowPriority(ffmpegCmd(ctx, args...)); err != nil {
		return fmt.Errorf("ffmpeg compose video: %w: %s", err, b)
	}
	return nil
}

// The container ships libx264. Some distribution builds provide OpenH264
// instead; both produce H.264 MP4 files playable by browsers.
func videoEncoder(ctx context.Context) ([]string, error) {
	b, err := ffmpegCmd(ctx, "-hide_banner", "-encoders").Output()
	if err != nil {
		return nil, fmt.Errorf("list video encoders: %w", err)
	}
	available := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			available[fields[1]] = true
		}
	}
	if available["libx264"] {
		return []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23"}, nil
	}
	if available["libopenh264"] {
		return []string{"-c:v", "libopenh264", "-b:v", "2M"}, nil
	}
	return nil, fmt.Errorf("video recording requires ffmpeg with libx264 or libopenh264")
}

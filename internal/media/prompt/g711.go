package prompt

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/shridarpatil/tring/internal/media/room"
)

// g711FrameBytes is one 20ms G.711 frame: 160 samples of one byte each.
const g711FrameBytes = 160

// LoadG711 decodes an audio file (prompts, hold music and TTS are stored as
// Ogg/Opus) to 20ms G.711 frames in law c, for a phone room. It runs ffmpeg,
// so callers cache the result rather than decoding per call.
func LoadG711(ctx context.Context, path string, c room.Codec) ([][]byte, error) {
	format := "alaw"
	if c == room.PCMU {
		format = "mulaw"
	}
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-threads", "1", "-i", path, "-vn", "-ac", "1", "-ar", "8000", "-f", format, "-")
	// An empty environment: prompt files can be tenant uploads, so a decoder
	// bug must not also reach the gateway's secrets (see record.ffmpegCmd).
	cmd.Env = []string{}
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("prompt: ffmpeg to %s: %w: %s", format, err, stderr.Bytes())
	}
	return SplitG711(out.Bytes()), nil
}

// SplitG711 cuts raw G.711 samples into 20ms frames, one per RTP packet. A
// short tail frame is dropped (under 20ms of audio).
func SplitG711(raw []byte) [][]byte {
	frames := make([][]byte, 0, len(raw)/g711FrameBytes)
	for len(raw) >= g711FrameBytes {
		frames = append(frames, raw[:g711FrameBytes:g711FrameBytes])
		raw = raw[g711FrameBytes:]
	}
	return frames
}

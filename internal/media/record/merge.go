package record

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ErrFFmpegMissing is returned by MergeStereo when ffmpeg isn't on PATH.
var ErrFFmpegMissing = errors.New("ffmpeg not found on PATH")

// MergeStereo merges a left and right mono Ogg/Opus leg into one stereo
// Ogg/Opus file (left→front-left, right→front-right) using ffmpeg. This is the
// finalize step that satisfies the "stereo, agent L / customer R" rule without
// transcoding on the hot path — the per-leg captures stay lossless and the mix
// happens once, after the call.
//
// Requires ffmpeg on PATH (the FreeSWITCH image already ships it; a host doing
// pion recording needs it installed). Returns ErrFFmpegMissing if absent so the
// caller can fall back to keeping the two per-leg files.
func MergeStereo(ctx context.Context, out, left, right string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return ErrFFmpegMissing
	}
	// join maps input 0's channel → FL and input 1's → FR (deterministic L/R,
	// unlike amerge which just interleaves).
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-i", left, "-i", right,
		"-filter_complex", "[0:a][1:a]join=inputs=2:channel_layout=stereo[a]",
		"-map", "[a]", "-c:a", "libopus", out,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg merge: %w: %s", err, b)
	}
	return nil
}

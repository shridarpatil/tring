package record

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
	// Map input 0's FIRST channel → FL and input 1's first channel → FR. The
	// explicit map is REQUIRED: each per-leg file carries a 2-channel Opus header
	// (record.opusChannels) though it holds one mono mic, and join WITHOUT a map
	// fills the stereo output entirely from input 0 — silently dropping input 1
	// (the customer), so both channels come out as the agent. `0.0`/`1.0` take
	// channel 0 of each leg regardless of its declared channel count.
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-i", left, "-i", right,
		"-filter_complex", "[0:a][1:a]join=inputs=2:channel_layout=stereo:map=0.0-FL|1.0-FR[a]",
		"-map", "[a]", "-c:a", "libopus", out,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg merge: %w: %s", err, b)
	}
	return nil
}

// AgentLeg is one agent's captured leg plus where it sits on the call timeline:
// Delay is the offset from the recording start (agent answer) at which this leg
// began — 0 for the first agent, the transfer gap for one that answered later.
type AgentLeg struct {
	Path  string
	Delay time.Duration
}

// MergeCall produces the single stereo recording for a call that passed through
// one OR MORE agents (a blind transfer routes the customer to a second agent):
// the customer's continuous leg goes RIGHT, and every agent leg is laid onto the
// LEFT channel at its own Delay offset so a later agent lands where they actually
// spoke — not stacked at t=0 and not overwriting the earlier agent (the bug this
// fixes: same-role legs shared one file, so only the last agent survived). For a
// single agent this reduces to MergeStereo's "agent L / customer R". Requires
// ffmpeg; returns ErrFFmpegMissing when absent so the caller can fall back.
func MergeCall(ctx context.Context, out, customer string, agents []AgentLeg) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return ErrFFmpegMissing
	}
	if len(agents) == 0 {
		return errors.New("MergeCall: no agent legs")
	}

	args := []string{"-y"}
	for _, a := range agents {
		args = append(args, "-i", a.Path)
	}
	args = append(args, "-i", customer) // customer is the LAST input

	// Each agent leg → mono (channel 0 of its 2-channel header, per opusChannels),
	// delayed to its offset. amix sums them onto one LEFT channel (normalize=0 keeps
	// full volume; blind-transfer legs don't overlap, so each occupies its own slot
	// with silence between). The customer input → mono RIGHT. join maps L|R → stereo.
	var fc strings.Builder
	labels := make([]string, len(agents))
	for i, a := range agents {
		lbl := fmt.Sprintf("la%d", i)
		labels[i] = "[" + lbl + "]"
		fc.WriteString(fmt.Sprintf("[%d:a]pan=mono|c0=c0", i))
		if ms := a.Delay.Milliseconds(); ms > 0 {
			fc.WriteString(fmt.Sprintf(",adelay=%d", ms))
		}
		fc.WriteString(fmt.Sprintf("[%s];", lbl))
	}
	if len(agents) == 1 {
		fc.WriteString(labels[0])
		fc.WriteString("anull[l];")
	} else {
		fc.WriteString(strings.Join(labels, ""))
		fc.WriteString(fmt.Sprintf("amix=inputs=%d:normalize=0:duration=longest[l];", len(agents)))
	}
	fc.WriteString(fmt.Sprintf("[%d:a]pan=mono|c0=c0[r];", len(agents)))
	fc.WriteString("[l][r]join=inputs=2:channel_layout=stereo:map=0.0-FL|1.0-FR[a]")

	args = append(args, "-filter_complex", fc.String(), "-map", "[a]", "-c:a", "libopus", out)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg merge-call: %w: %s", err, b)
	}
	return nil
}

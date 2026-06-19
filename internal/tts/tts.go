// Package tts synthesizes IVR prompt text to speech with a local Piper binary,
// caching the result as 48 kHz mono Opus-Ogg — the one format both media planes
// already play (the pion prompt player via prompt.LoadOggOpus, and FreeSWITCH
// via `playback <file>`). So an IVR node with text but no uploaded audio gets
// spoken on web AND phone from a single synthesized file.
//
// It shells out to `piper` then `ffmpeg` (the same ffmpeg the upload path uses,
// see internal/api/handlers_audio.go), mirroring the project's subprocess-audio
// pattern — no cgo/Opus-encoder dependency.
//
// The engine auto-enables when the piper binary, the voice model, and ffmpeg all
// resolve at startup; otherwise it's a graceful no-op (Enabled()==false), so a
// host-dev box without Piper behaves like before (silent text nodes) rather than
// erroring. A nil *Engine is also a valid no-op.
package tts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ErrDisabled is returned by Synthesize when the engine isn't ready (no Piper /
// model / ffmpeg). Callers treat it as "skip the prompt", not a call failure.
var ErrDisabled = errors.New("tts: engine disabled (piper/model/ffmpeg not found)")

// Config builds an Engine.
type Config struct {
	PiperBin string // piper binary (path or name on PATH)
	Model    string // path to the voice .onnx model (its .json sits beside it)
	Voice    string // default speaker id for multi-speaker models ("" → speaker 0)
	CacheDir string // where synthesized <hash>.ogg files are cached
	Logger   *slog.Logger
}

// Engine synthesizes text → cached Opus-Ogg via Piper + ffmpeg.
type Engine struct {
	piperBin string
	ffmpeg   string
	model    string
	voice    string
	cacheDir string
	log      *slog.Logger
	ready    bool

	locks sync.Map // synth key → *sync.Mutex (dedupe concurrent synth of the same phrase)
}

// New resolves Piper + ffmpeg + the model and reports whether TTS is usable. It
// never fails: an unresolved dependency just leaves Enabled()==false.
func New(cfg Config) *Engine {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	e := &Engine{model: cfg.Model, voice: cfg.Voice, cacheDir: cfg.CacheDir, log: log.With("component", "tts")}

	piper, perr := exec.LookPath(cfg.PiperBin)
	ff, fferr := exec.LookPath("ffmpeg")
	modelOK := cfg.Model != "" && fileExists(cfg.Model)
	if perr == nil && fferr == nil && modelOK && cfg.CacheDir != "" {
		if err := os.MkdirAll(cfg.CacheDir, 0o775); err != nil {
			e.log.Warn("tts disabled: cache dir not writable", "dir", cfg.CacheDir, "err", err)
			return e
		}
		e.piperBin, e.ffmpeg, e.ready = piper, ff, true
		e.log.Info("tts enabled", "piper", piper, "model", cfg.Model, "cache", cfg.CacheDir)
		return e
	}
	e.log.Info("tts disabled (text-only IVR nodes will be silent)",
		"piper_err", perr, "ffmpeg_err", fferr, "model_ok", modelOK)
	return e
}

// Enabled reports whether synthesis is available.
func (e *Engine) Enabled() bool { return e != nil && e.ready }

// Synthesize renders text to a cached 48 kHz mono Opus-Ogg file and returns its
// path. Idempotent + cached by sha256(model|voice|text): a repeated phrase is a
// no-op stat. voice overrides the engine default speaker ("" → use default).
func (e *Engine) Synthesize(ctx context.Context, text, voice string) (string, error) {
	if !e.Enabled() {
		return "", ErrDisabled
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ErrDisabled
	}
	if voice == "" {
		voice = e.voice
	}

	sum := sha256.Sum256([]byte(e.model + "|" + voice + "|" + text))
	key := hex.EncodeToString(sum[:])
	out := filepath.Join(e.cacheDir, key+".ogg")
	if fileExists(out) {
		return out, nil
	}

	// Serialize synthesis of the same phrase so two concurrent callers don't both
	// shell out to piper for it.
	lk, _ := e.locks.LoadOrStore(key, &sync.Mutex{})
	mu := lk.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if fileExists(out) { // another goroutine produced it while we waited
		return out, nil
	}

	tmpWav := out + ".tmp.wav"
	defer os.Remove(tmpWav)
	pargs := []string{"-q", "-m", e.model, "-f", tmpWav}
	if voice != "" {
		pargs = append(pargs, "-s", voice)
	}
	pcmd := exec.CommandContext(ctx, e.piperBin, pargs...)
	pcmd.Stdin = strings.NewReader(text) // piper reads the prompt text from stdin
	if b, err := pcmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("tts: piper: %w: %s", err, strings.TrimSpace(string(b)))
	}

	// WAV → 48 kHz mono Opus-Ogg (same flags as the upload transcode path), to a
	// temp file then atomic-renamed so a concurrent reader never sees a partial.
	tmpOgg := out + ".tmp.ogg"
	fcmd := exec.CommandContext(ctx, e.ffmpeg, "-y", "-i", tmpWav,
		"-ac", "1", "-ar", "48000", "-c:a", "libopus", tmpOgg)
	if b, err := fcmd.CombinedOutput(); err != nil {
		os.Remove(tmpOgg)
		return "", fmt.Errorf("tts: ffmpeg: %w: %s", err, strings.TrimSpace(string(b)))
	}
	if err := os.Rename(tmpOgg, out); err != nil {
		os.Remove(tmpOgg)
		return "", fmt.Errorf("tts: cache write: %w", err)
	}
	return out, nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

package tts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// stubPiper writes a shell script that mimics `piper -m M -f OUT.wav` by
// generating a short silent WAV with ffmpeg, so we can exercise the cache +
// ffmpeg→Opus pipeline without the real (large) Piper binary/model.
func stubPiper(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "piper")
	script := `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in -f) out="$2"; shift;; esac
  shift
done
cat >/dev/null   # consume the prompt text on stdin
exec ffmpeg -nostdin -f lavfi -i anullsrc=r=22050:cl=mono -t 0.2 -y "$out" 2>/dev/null
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub piper: %v", err)
	}
	return path
}

func TestSynthesizeCachesOgg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	piper := stubPiper(t, dir)
	model := filepath.Join(dir, "voice.onnx")
	if err := os.WriteFile(model, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "cache")

	e := New(Config{PiperBin: piper, Model: model, CacheDir: cache})
	if !e.Enabled() {
		t.Fatal("engine should be enabled (stub piper + model + ffmpeg present)")
	}

	out, err := e.Synthesize(context.Background(), "Hello there", "")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty ogg at %s: %v", out, err)
	}
	if filepath.Ext(out) != ".ogg" {
		t.Fatalf("expected .ogg output, got %s", out)
	}

	// Second call for the same text is a cache hit → identical path, no temp files left.
	out2, err := e.Synthesize(context.Background(), "Hello there", "")
	if err != nil || out2 != out {
		t.Fatalf("cache hit expected: out2=%s err=%v", out2, err)
	}
	entries, _ := os.ReadDir(cache)
	for _, en := range entries {
		if filepath.Ext(en.Name()) == ".wav" || filepath.Ext(en.Name()) == ".tmp" {
			t.Fatalf("leftover temp file in cache: %s", en.Name())
		}
	}
}

func TestDisabledEngineIsNoOp(t *testing.T) {
	// Missing model → not ready.
	e := New(Config{PiperBin: "definitely-not-a-real-binary", Model: "/nope.onnx", CacheDir: t.TempDir()})
	if e.Enabled() {
		t.Fatal("engine must be disabled when piper/model are missing")
	}
	if _, err := e.Synthesize(context.Background(), "hi", ""); err != ErrDisabled {
		t.Fatalf("want ErrDisabled, got %v", err)
	}

	// A nil engine is also a safe no-op.
	var nilEng *Engine
	if nilEng.Enabled() {
		t.Fatal("nil engine must report disabled")
	}
}

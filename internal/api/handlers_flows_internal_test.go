package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whatomate/tring/internal/tts"
)

// stubPiperEngine builds a tts.Engine backed by a fake piper (ffmpeg-generated
// silence), so the flow-prerender logic is testable without the real binary.
func stubPiperEngine(t *testing.T) *tts.Engine {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	dir := t.TempDir()
	piper := filepath.Join(dir, "piper")
	script := `#!/bin/sh
out=""
while [ $# -gt 0 ]; do case "$1" in -f) out="$2"; shift;; esac; shift; done
cat >/dev/null
exec ffmpeg -nostdin -f lavfi -i anullsrc=r=22050:cl=mono -t 0.2 -y "$out" 2>/dev/null
`
	if err := os.WriteFile(piper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(dir, "voice.onnx")
	if err := os.WriteFile(model, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := tts.New(tts.Config{PiperBin: piper, Model: model, CacheDir: filepath.Join(dir, "tts")})
	if !eng.Enabled() {
		t.Fatal("stub engine should be enabled")
	}
	return eng
}

func TestPrerenderFlowAudio(t *testing.T) {
	s := &Server{tts: stubPiperEngine(t), audioDir: "/sounds", log: slog.Default()}

	def := json.RawMessage(`{"version":2,"entry_node":"s","nodes":[
		{"id":"g","type":"greeting","label":"Hi","position":{"x":1,"y":2},"config":{"greeting_text":"Welcome to support"}},
		{"id":"v","type":"greeting","config":{"greeting_text":"Hello {{name}}"}},
		{"id":"u","type":"greeting","config":{"greeting_text":"ignored","audio_file":"/sounds/t/up.ogg"}}
	],"edges":[{"from":"s","to":"g","condition":"default"}]}`)

	out := s.prerenderFlowAudio(context.Background(), def)

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	nodes := root["nodes"].([]any)
	cfg := func(i int) map[string]any { return nodes[i].(map[string]any)["config"].(map[string]any) }

	// Static-text node → audio_file stamped under /sounds/tts/.
	if af, _ := cfg(0)["audio_file"].(string); !strings.HasPrefix(af, "/sounds/tts/") || !strings.HasSuffix(af, ".ogg") {
		t.Fatalf("greeting audio_file = %q, want /sounds/tts/<hash>.ogg", af)
	}
	// {{var}} text → NOT synthesized (can only render at call time).
	if _, ok := cfg(1)["audio_file"]; ok {
		t.Fatal("node with {{vars}} must not be synthesized")
	}
	// Uploaded audio_file → preserved (upload wins over text).
	if af, _ := cfg(2)["audio_file"].(string); af != "/sounds/t/up.ogg" {
		t.Fatalf("uploaded audio_file overwritten: %q", af)
	}
	// Non-config fields round-trip intact (no field loss from the map mutation).
	if root["entry_node"] != "s" || root["version"].(float64) != 2 {
		t.Fatal("definition top-level fields lost")
	}
	if n0 := nodes[0].(map[string]any); n0["label"] != "Hi" || n0["position"] == nil {
		t.Fatal("node label/position lost")
	}
	if edges := root["edges"].([]any); len(edges) != 1 {
		t.Fatal("edges lost")
	}
}

func TestPrerenderFlowAudioDisabledIsPassThrough(t *testing.T) {
	s := &Server{tts: nil, log: slog.Default()} // no engine
	def := json.RawMessage(`{"nodes":[{"id":"g","config":{"greeting_text":"hi"}}]}`)
	if out := s.prerenderFlowAudio(context.Background(), def); string(out) != string(def) {
		t.Fatalf("disabled engine should pass definition through unchanged; got %s", out)
	}
}

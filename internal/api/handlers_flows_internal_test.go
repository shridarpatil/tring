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

	"github.com/shridarpatil/tring/internal/tts"
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

// Editing synthesized text to include {{vars}} must drop the audio rendered
// from the old text; it used to stay attached and keep playing the old words.
// The reverse edit (vars removed) synthesizes the new text.
func TestPrerenderFlowAudioDropsStaleTTSForVars(t *testing.T) {
	s := &Server{tts: stubPiperEngine(t), audioDir: "/sounds", log: slog.Default()}
	nodeCfg := func(def string) map[string]any {
		t.Helper()
		var root map[string]any
		if err := json.Unmarshal(s.prerenderFlowAudio(context.Background(), json.RawMessage(def)), &root); err != nil {
			t.Fatal(err)
		}
		return root["nodes"].([]any)[0].(map[string]any)["config"].(map[string]any)
	}

	cfg := nodeCfg(`{"nodes":[{"id":"g","config":{"greeting_text":"Hello {{name}}","audio_file":"/sounds/tts/old.ogg"}}]}`)
	if af, ok := cfg["audio_file"]; ok {
		t.Fatalf("stale synthesized audio kept for {{vars}} text: %v", af)
	}
	// An uploaded file is not TTS-managed and stays.
	cfg = nodeCfg(`{"nodes":[{"id":"g","config":{"greeting_text":"Hello {{name}}","audio_file":"/sounds/t/up.ogg"}}]}`)
	if af := cfg["audio_file"]; af != "/sounds/t/up.ogg" {
		t.Fatalf("uploaded audio_file dropped: %v", af)
	}
	cfg = nodeCfg(`{"nodes":[{"id":"g","config":{"greeting_text":"Hello there"}}]}`)
	if af, _ := cfg["audio_file"].(string); !strings.HasPrefix(af, "/sounds/tts/") {
		t.Fatalf("plain text not synthesized: %q", af)
	}
}

func TestPrerenderFlowAudioDisabledIsPassThrough(t *testing.T) {
	s := &Server{tts: nil, log: slog.Default()} // no engine
	def := json.RawMessage(`{"nodes":[{"id":"g","config":{"greeting_text":"hi"}}]}`)
	if out := s.prerenderFlowAudio(context.Background(), def); string(out) != string(def) {
		t.Fatalf("disabled engine should pass definition through unchanged; got %s", out)
	}
}

// A menu's invalid-input message is rendered to its own audio field on save,
// beside (not instead of) the menu prompt; an uploaded invalid message wins,
// and clearing the text drops a message we rendered.
func TestPrerenderFlowAudioRendersMenuInvalidMessage(t *testing.T) {
	s := &Server{tts: stubPiperEngine(t), audioDir: "/sounds", log: slog.Default()}

	def := json.RawMessage(`{"version":2,"entry_node":"m","nodes":[
		{"id":"m","type":"menu","config":{"greeting_text":"Press 1","invalid_text":"Sorry, that is not an option"}},
		{"id":"u","type":"menu","config":{"invalid_text":"ignored","invalid_audio_file":"/sounds/t/oops.ogg"}},
		{"id":"c","type":"menu","config":{"invalid_text":"","invalid_audio_file":"/sounds/tts/old.ogg"}}
	],"edges":[]}`)

	var root map[string]any
	if err := json.Unmarshal(s.prerenderFlowAudio(context.Background(), def), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	nodes := root["nodes"].([]any)
	cfg := func(i int) map[string]any { return nodes[i].(map[string]any)["config"].(map[string]any) }

	prompt, _ := cfg(0)["audio_file"].(string)
	invalid, _ := cfg(0)["invalid_audio_file"].(string)
	if !strings.HasPrefix(invalid, "/sounds/tts/") || invalid == prompt {
		t.Fatalf("invalid_audio_file = %q (prompt %q), want its own /sounds/tts/ rendering", invalid, prompt)
	}
	if af, _ := cfg(1)["invalid_audio_file"].(string); af != "/sounds/t/oops.ogg" {
		t.Fatalf("uploaded invalid message overwritten: %q", af)
	}
	if _, ok := cfg(2)["invalid_audio_file"]; ok {
		t.Fatal("rendered invalid message kept after its text was cleared")
	}
}

package signaling

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/media/room"
)

func writeTone(t *testing.T, path, seconds string) {
	t.Helper()
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration="+seconds,
		"-ac", "1", "-ar", "48000", "-c:a", "libopus", path).CombinedOutput(); err != nil {
		t.Fatalf("gen tone: %v: %s", err, b)
	}
}

// Phone rooms decode a prompt to G.711 once, not per call, and decode again
// only when the file changes.
func TestLoadAudioCachesG711(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hold.ogg")
	writeTone(t, path, "1")
	var cache g711Cache

	first, err := loadAudio(&cache, dir, path, room.PCMA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := loadAudio(&cache, dir, "hold.ogg", room.PCMA) // relative form, same file
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.m) != 1 || &first[0][0] != &again[0][0] {
		t.Fatalf("second load decoded again (cache entries %d)", len(cache.m))
	}
	if opus, err := loadAudio(&cache, dir, path, room.Opus); err != nil || len(opus) == 0 || len(cache.m) != 1 {
		t.Fatalf("Opus load = %d frames, %v; cache entries %d (Opus must not be cached)", len(opus), err, len(cache.m))
	}

	writeTone(t, path, "2") // a replaced upload
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	replaced, err := loadAudio(&cache, dir, path, room.PCMA)
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) <= len(first) {
		t.Fatalf("replaced file served stale frames: %d, first was %d", len(replaced), len(first))
	}
}

// The open stays inside the audio dir whatever the stored path says.
func TestLoadAudioConfinedToDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadAudio(&g711Cache{}, dir, "../../../etc/passwd", room.PCMA); err == nil {
		t.Fatal("loaded a file outside the audio dir")
	}
}

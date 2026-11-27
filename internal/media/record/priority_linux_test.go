package record

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFFmpegRunsAtLowPriority: recording encodes share the gateway's CPU with
// live calls, so every ffmpeg thread — including the encoder/decoder threads it
// spawns after starting — must run at the lowered priority.
func TestFFmpegRunsAtLowPriority(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := ffmpegCmd(ctx, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x480:rate=30",
		"-t", "30", "-c:v", "mpeg4", "-threads", "4", "-f", "null", "-")
	if err := startLowPriority(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()

	tasks := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "task")
	var ids []os.DirEntry
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ids, _ = os.ReadDir(tasks); len(ids) > 2 {
			break
		}
	}
	if len(ids) <= 2 {
		t.Fatalf("ffmpeg started %d threads, want several to check", len(ids))
	}
	for _, id := range ids {
		b, err := os.ReadFile(filepath.Join(tasks, id.Name(), "stat"))
		if err != nil {
			continue // thread exited
		}
		// Fields after "(comm)" start at field 3 (state); nice is field 19.
		f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
		if nice, _ := strconv.Atoi(f[16]); nice != ffmpegNice {
			t.Errorf("thread %s nice = %d, want %d", id.Name(), nice, ffmpegNice)
		}
	}
}

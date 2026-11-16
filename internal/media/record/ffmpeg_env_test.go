package record

import (
	"context"
	"testing"
)

// TestFFmpegGetsNoSecrets: ffmpeg decodes untrusted callers' media, so it must
// not inherit the gateway's environment (JWT secret, DSN, ESL password).
func TestFFmpegGetsNoSecrets(t *testing.T) {
	t.Setenv("TRING_JWT_SECRET", "must-not-leak")
	cmd := ffmpegCmd(context.Background(), "-version")
	if env := cmd.Environ(); len(env) != 0 {
		t.Fatalf("ffmpeg env = %v, want empty", env)
	}
}

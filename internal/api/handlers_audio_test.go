package api

import "testing"

// diskPathFor maps an asset's playback path to where the API reads it on disk.
// The playback dir and the disk dir are the same shared mount (audio_dir) — the
// gateway plays prompts from the dir the API writes them to — so it's identity.
func TestDiskPathFor(t *testing.T) {
	s := &Server{audioDir: "/var/lib/tring/sounds"}
	fs := "/var/lib/tring/sounds/tenant-1/abc.wav"
	if got := s.diskPathFor(fs); got != fs {
		t.Fatalf("got %q, want %q", got, fs)
	}
}

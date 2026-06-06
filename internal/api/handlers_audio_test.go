package api

import "testing"

// diskPathFor maps an asset's FS-visible path back to where the API reads it.
// Equal dirs (standard docker) are a passthrough; differing dirs (host-run dev)
// swap the prefix so the bytes are found on the API's own filesystem.
func TestDiskPathFor(t *testing.T) {
	// Standard docker: audio_dir == audio_fs_dir.
	same := &Server{audioDir: "/usr/share/freeswitch/sounds/tring", audioFSDir: "/usr/share/freeswitch/sounds/tring"}
	fs := "/usr/share/freeswitch/sounds/tring/tenant-1/abc.wav"
	if got := same.diskPathFor(fs); got != fs {
		t.Fatalf("same dirs: got %q, want %q", got, fs)
	}

	// Host-run dev: API writes locally, FS plays from the container path.
	split := &Server{audioDir: "/home/dev/sounds", audioFSDir: "/usr/share/freeswitch/sounds/tring"}
	want := "/home/dev/sounds/tenant-1/abc.wav"
	if got := split.diskPathFor(fs); got != want {
		t.Fatalf("split dirs: got %q, want %q", got, want)
	}
}

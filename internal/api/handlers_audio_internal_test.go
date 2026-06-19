package api

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewDiskPathConfinement(t *testing.T) {
	s := &Server{audioDir: "/disk", audioFSDir: "/fs"}
	root := filepath.Clean("/disk")

	cases := []struct {
		name   string
		path   string
		wantOK bool
		want   string // exact disk path when relevant
	}{
		{"asset", "/fs/tenant/x.ogg", true, "/disk/tenant/x.ogg"},
		{"tts", "/fs/tts/abc.ogg", true, "/disk/tts/abc.ogg"},
		{"traversal-dropped", "/fs/../../../etc/passwd", true, "/disk/etc/passwd"},
		{"abs-escape", "/etc/passwd", true, "/disk/etc/passwd"},
		{"stream-url", "local_stream://moh", false, ""},
		{"http-url", "http://evil/x.ogg", false, ""},
		{"empty", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := s.previewDiskPath(c.path)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, c.wantOK, got)
			}
			if !ok {
				return
			}
			if got != c.want {
				t.Fatalf("path = %q, want %q", got, c.want)
			}
			// Belt-and-suspenders: the result must never escape audioDir.
			if got != root && !strings.HasPrefix(got, root+string(filepath.Separator)) {
				t.Fatalf("path %q escaped audioDir %q", got, root)
			}
		})
	}
}

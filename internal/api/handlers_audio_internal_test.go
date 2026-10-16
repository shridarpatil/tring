package api

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPreviewDiskPathConfinement(t *testing.T) {
	s := &Server{audioDir: "/disk"}
	root := filepath.Clean("/disk")
	own := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	other := "22222222-2222-2222-2222-222222222222"

	cases := []struct {
		name   string
		path   string
		wantOK bool
		want   string // exact disk path when relevant
	}{
		{"own asset", "/disk/" + own.String() + "/x.ogg", true, "/disk/" + own.String() + "/x.ogg"},
		{"tts", "/disk/tts/abc.ogg", true, "/disk/tts/abc.ogg"},
		{"other tenant", "/disk/" + other + "/x.ogg", false, ""},
		{"other tenant via traversal", "/disk/" + own.String() + "/../" + other + "/x.ogg", false, ""},
		{"traversal out", "/disk/../../../etc/passwd", false, ""},
		{"abs-escape", "/etc/passwd", false, ""},
		{"audio dir root", "/disk/x.ogg", false, ""},
		{"stream-url", "local_stream://moh", false, ""},
		{"http-url", "http://evil/x.ogg", false, ""},
		{"empty", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := s.previewDiskPath(own, c.path)
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

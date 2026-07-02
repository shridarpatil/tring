package signaling

import "testing"

// TestHoldFile is the pion-plane hold-music playability rule: only a real local
// audio file plays. The FreeSWITCH-only "local_stream://moh" sentinel (and any
// other URL scheme) has no pion equivalent, so it maps to "" = silence — which
// is also the "no hold music configured" case. This is what makes visitor hold
// music play ONLY when a tenant has uploaded a file.
func TestHoldFile(t *testing.T) {
	cases := []struct {
		name string
		moh  string
		want string
	}{
		{"empty", "", ""},
		{"freeswitch stream sentinel", "local_stream://moh", ""},
		{"any url scheme", "http://example.com/moh.ogg", ""},
		{"uploaded file path", "/var/lib/tring/sounds/t/uuid.ogg", "/var/lib/tring/sounds/t/uuid.ogg"},
		{"relative file", "custom/hold.ogg", "custom/hold.ogg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := holdFile(c.moh); got != c.want {
				t.Fatalf("holdFile(%q) = %q, want %q", c.moh, got, c.want)
			}
		})
	}
}

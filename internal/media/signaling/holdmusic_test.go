package signaling

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
)

// stubHoldMusic returns a fixed tenant system hold music.
type stubHoldMusic struct{ moh string }

func (s stubHoldMusic) TenantHoldMusic(context.Context, uuid.UUID) (string, error) {
	return s.moh, nil
}

// The ring/transfer hold music must fall back to the tenant's system hold music
// when the team supplies none (or a stream sentinel pion can't play) — otherwise a
// direct-agent transfer (no team config → moh="") leaves the waiting caller in
// silence instead of ringback. See pionRing.effectiveMOH.
func TestEffectiveMOHFallsBackToTenantHoldMusic(t *testing.T) {
	tenant := uuid.New()
	rid := room.RoomID(tenant.String() + "/call")
	s := &Server{cfg: Config{HoldMusic: stubHoldMusic{moh: "tenant.ogg"}}, log: slog.Default()}

	cases := []struct {
		name    string
		teamMOH string
		want    string
	}{
		{"team moh set wins", "team.ogg", "team.ogg"},
		{"no team moh falls back to tenant (direct-agent transfer)", "", "tenant.ogg"},
		{"stream sentinel falls back to tenant", "local_stream://moh", "tenant.ogg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &pionRing{s: s, roomID: rid, moh: c.teamMOH}
			if got := p.effectiveMOH(); got != c.want {
				t.Fatalf("effectiveMOH(team=%q) = %q, want %q", c.teamMOH, got, c.want)
			}
		})
	}
}

// With no tenant hold music configured either, the fallback yields "" (silence) —
// nothing is playable, which is the correct no-op.
func TestEffectiveMOHNoneConfigured(t *testing.T) {
	tenant := uuid.New()
	rid := room.RoomID(tenant.String() + "/call")
	s := &Server{cfg: Config{HoldMusic: stubHoldMusic{moh: ""}}, log: slog.Default()}
	p := &pionRing{s: s, roomID: rid, moh: ""}
	if got := p.effectiveMOH(); got != "" {
		t.Fatalf("effectiveMOH with nothing configured = %q, want \"\"", got)
	}
}

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

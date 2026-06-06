package orchestrator

import "testing"

// effectiveMOH: a real per-team override wins; else the tenant's uploaded hold
// music; else the built-in default. The schema default (local_stream://moh) is
// treated as "no override".
func TestEffectiveMOH(t *testing.T) {
	const builtin = "local_stream://moh"
	cases := []struct {
		teamMOH, tenant, want string
	}{
		{"", "", builtin},
		{builtin, "", builtin},
		{builtin, "/snd/tenant.wav", "/snd/tenant.wav"},       // tenant upload, no team override
		{"", "/snd/tenant.wav", "/snd/tenant.wav"},            // same, empty team moh
		{"/snd/team.wav", "/snd/tenant.wav", "/snd/team.wav"}, // team override beats tenant
		{"/snd/team.wav", "", "/snd/team.wav"},
	}
	for _, c := range cases {
		if got := effectiveMOH(c.teamMOH, c.tenant); got != c.want {
			t.Errorf("effectiveMOH(%q,%q) = %q, want %q", c.teamMOH, c.tenant, got, c.want)
		}
	}
}

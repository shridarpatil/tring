package mask

import "testing"

func TestNumber(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"+919876543210", "+91••••••3210"},
		{"919876543210", "91••••••3210"},
		{"09876543210", "09•••••3210"},
		{"1234567", "12•4567"},
		{"123456", "••••••"},
		{"+1001", "+••••"},
		{"+91 98765-43210", "+91 •••••-•3210"},
		{"", ""},
		{"+", "+"},
		{"Web visitor", "Web visitor"},
		{"sip:+919876543210@example.com", "sip:+919876543210@example.com"},
		{"alice", "alice"},
	} {
		if got := Number(tc.in); got != tc.want {
			t.Errorf("Number(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSettingsFor(t *testing.T) {
	roles := []struct {
		role    string
		manager bool
		name    string
	}{
		{"agent", false, "agent"},
		{"agent", true, "manager"},
		{"supervisor", false, "supervisor"},
		{"supervisor", true, "supervisor"},
		{"admin", false, "admin"},
		{"admin", true, "admin"},
	}
	flags := []Settings{
		{},
		{Agents: true},
		{Managers: true},
		{Supervisors: true},
		{Agents: true, Managers: true, Supervisors: true},
	}
	for _, s := range flags {
		for _, r := range roles {
			want := map[string]bool{
				"agent": s.Agents, "manager": s.Managers, "supervisor": s.Supervisors, "admin": false,
			}[r.name]
			if got := s.For(r.role, r.manager); got != want {
				t.Errorf("%+v.For(%s, manager=%v) = %v, want %v", s, r.role, r.manager, got, want)
			}
		}
	}
	if (Settings{}).Any() || !(Settings{Managers: true}).Any() {
		t.Error("Any")
	}
}

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

func TestInText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Customer +919876543210", "Customer +91••••••3210"},
		{"+919876543210", "+91••••••3210"},
		{"1001", "••••"}, // a whole-label number is masked like Number
		{"Lead 9876543210 (VIP)", "Lead 98••••3210 (VIP)"},
		{"Call +91 98765-43210 now", "Call +91 •••••-•3210 now"},
		{"a 9876543 b 12345678", "a 98•6543 b 12••5678"},
		{"ext 12", "ext 12"},
		{"Order 123456 for Bob", "Order 123456 for Bob"},
		{"Sales queue", "Sales queue"},
		{"", ""},
	} {
		if got := InText(tc.in); got != tc.want {
			t.Errorf("InText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCallNumbers(t *testing.T) {
	for _, tc := range []struct {
		dir, from, to, wantFrom, wantTo string
	}{
		{"inbound", "+919876543210", "+918000001234", "+91••••••3210", "+918000001234"},
		{"outbound", "+918000001234", "+919876543210", "+918000001234", "+91••••••3210"},
		{"", "+919876543210", "+918000001234", "+91••••••3210", "+91••••••1234"},
	} {
		f, to := CallNumbers(tc.dir, tc.from, tc.to)
		if f != tc.wantFrom || to != tc.wantTo {
			t.Errorf("CallNumbers(%s) = %q %q, want %q %q", tc.dir, f, to, tc.wantFrom, tc.wantTo)
		}
	}
}

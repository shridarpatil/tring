package mask

import "testing"

func TestNumber(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"+919876543210", "+91••••••3210"},
		{"919876543210", "91••••••3210"},
		{"09876543210", "09•••••3210"},
		{"1234567", "12•4567"},
		{"123456", "123456"},
		{"+1001", "+1001"},
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

func TestCallNumbers(t *testing.T) {
	for _, tc := range []struct {
		dir, from, to, wantFrom, wantTo string
	}{
		{"inbound", "+919876543210", "+918000001234", "+91••••••3210", "+918000001234"},
		{"outbound", "+918000001234", "+919876543210", "+918000001234", "+91••••••3210"},
		{"", "+919876543210", "+918000001234", "+91••••••3210", "+91••••••1234"},
		// Text is never searched for numbers: dates, order ids and versions
		// stay readable, and so does a number the embedder put inside text.
		{"inbound", "Order 2026-11-07", "+918000001234", "Order 2026-11-07", "+918000001234"},
		{"inbound", "v1.2.3456789", "+918000001234", "v1.2.3456789", "+918000001234"},
		{"inbound", "Ravi 9876543210", "+918000001234", "Ravi 9876543210", "+918000001234"},
		{"inbound", "(022) 2345 6789", "+918000001234", "(02•) •••• 6789", "+918000001234"},
		{"inbound", "98765.43210", "+918000001234", "98•••.•3210", "+918000001234"},
		{"inbound", "98765  43210", "+918000001234", "98•••  •3210", "+918000001234"},
	} {
		f, to := CallNumbers(tc.dir, tc.from, tc.to)
		if f != tc.wantFrom || to != tc.wantTo {
			t.Errorf("CallNumbers(%s) = %q %q, want %q %q", tc.dir, f, to, tc.wantFrom, tc.wantTo)
		}
	}
}

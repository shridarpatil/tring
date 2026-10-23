package signaling

import (
	"strings"
	"testing"
)

// A web call's CDR "from" must not let visitor-typed text pass for an identity.
func TestWebCallerFrom(t *testing.T) {
	cases := []struct {
		name, ref, display, want string
	}{
		{"client_ref wins", "cust-42", "Alice", "cust-42"},
		{"name is labelled, not an identity", "", "Alice", "Web visitor (name: Alice)"},
		{"phone-number-looking name still labelled", "", "+919876543210", "Web visitor (name: +919876543210)"},
		{"no name", "", "", "Web visitor"},
		{"blank name", "", "  \t ", "Web visitor"},
		{"control chars stripped", "", "Al\nice\x00\r", "Web visitor (name: Alice)"},
		{"bidi override stripped", "", "a‮b", "Web visitor (name: ab)"},
		{"truncated", "", strings.Repeat("x", 500), "Web visitor (name: " + strings.Repeat("x", maxVisitorNameRunes) + ")"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := webCallerFrom(c.ref, c.display); got != c.want {
				t.Fatalf("webCallerFrom(%q,%q) = %q, want %q", c.ref, c.display, got, c.want)
			}
		})
	}
}

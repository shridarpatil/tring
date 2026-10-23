package dial

import (
	"errors"
	"testing"
)

func TestIndia(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr error
	}{
		// canonical
		{"+919876543210", "+919876543210", nil},
		// with 91 no plus
		{"919876543210", "+919876543210", nil},
		// national format with leading 0
		{"09876543210", "+919876543210", nil},
		// bare 10-digit mobile
		{"9876543210", "+919876543210", nil},
		// user-formatted (spaces, dashes, parens)
		{"+91 98765-43210", "+919876543210", nil},
		{"(+91) 9876 543 210", "+919876543210", nil},
		// other valid Indian mobile prefixes
		{"6123456789", "+916123456789", nil},
		{"7123456789", "+917123456789", nil},
		{"8123456789", "+918123456789", nil},
		// rejects
		{"", "", ErrEmpty},
		{"   ", "", ErrEmpty},
		// 5xxx is not a valid Indian mobile prefix
		{"5123456789", "", ErrNotIndian},
		// US number
		{"+14155551234", "", ErrNotIndian},
		// too short
		{"123", "", ErrNotIndian},
		// too long
		{"+9198765432101", "", ErrNotIndian},
		// not even digits
		{"call alice", "", ErrNotIndian},
	}
	for _, c := range cases {
		got, err := India(c.in)
		if !errors.Is(err, c.wantErr) {
			t.Errorf("India(%q) err = %v, want %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("India(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPassthrough(t *testing.T) {
	if got, err := Passthrough("9196"); err != nil || got != "9196" {
		t.Errorf("Passthrough(9196) = (%q, %v), want (\"9196\", nil)", got, err)
	}
	if _, err := Passthrough("  "); !errors.Is(err, ErrEmpty) {
		t.Errorf("Passthrough(blank) should be ErrEmpty, got %v", err)
	}
}

// Passthrough output goes straight into a FreeSWITCH dial string, so anything
// beyond digits and a leading + must be rejected, not passed through.
func TestPassthroughRejectsDialStringSyntax(t *testing.T) {
	ok := map[string]string{"9196": "9196", " +14155551234 ": "+14155551234", "5000": "5000"}
	for in, want := range ok {
		if got, err := Passthrough(in); err != nil || got != want {
			t.Errorf("Passthrough(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"1|sofia/gateway/t_000000000000/19005550100",
		"1\n\napi status\n\n",
		"1,origination_caller_id_number=1",
		"[x=y]1", "1 2", "12", "+1234567890123456", "abc", "1}",
	}
	for _, in := range bad {
		if _, err := Passthrough(in); !errors.Is(err, ErrBadFormat) {
			t.Errorf("Passthrough(%q) err = %v, want ErrBadFormat", in, err)
		}
	}
}

func TestIsNumber(t *testing.T) {
	for _, s := range []string{"+912265915000", "02265915000", "9196"} {
		if !IsNumber(s) {
			t.Errorf("IsNumber(%q) = false", s)
		}
	}
	for _, s := range []string{"", "+91 22", "91|x", "1\n", "+", "12"} {
		if IsNumber(s) {
			t.Errorf("IsNumber(%q) = true", s)
		}
	}
}

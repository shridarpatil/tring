package freeswitch

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCIDRs(t *testing.T) {
	got, err := ParseCIDRs(" 10.0.0.5, 198.51.100.13/29,,2001:db8::1 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.5/32", "198.51.100.8/29", "2001:db8::1/128"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	if got, err := ParseCIDRs(""); err != nil || len(got) != 0 {
		t.Errorf("empty: got %v, %v", got, err)
	}
}

func TestParseCIDRsRejects(t *testing.T) {
	for _, in := range []string{
		"sbc.example.com",
		"10.0.0.0/33",
		"0.0.0.0/0", // switches the ACL off
		"::/0",
		"fe80::1%eth0",
		`10.0.0.1"/><node type="allow" cidr="0.0.0.0/0`,
		"10.0.0.1\n0.0.0.0/1",
	} {
		if _, err := ParseCIDRs(in); err == nil {
			t.Errorf("ParseCIDRs(%q) accepted", in)
		}
	}
}

func TestWriteCarrierACL(t *testing.T) {
	old := GatewayDir
	GatewayDir = t.TempDir()
	defer func() { GatewayDir = old }()

	ps := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.8/29"),
		netip.MustParsePrefix("10.0.0.5/32"),
		netip.MustParsePrefix("198.51.100.8/29"),
	}
	if err := WriteCarrierACL(ps); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(GatewayDir, CarrierACLFile)
	b, _ := os.ReadFile(path)
	if want := "10.0.0.5/32\n198.51.100.8/29\n"; string(b) != want {
		t.Errorf("file = %q, want %q", b, want)
	}

	// No addresses on any trunk: empty file, read by the script as "any source".
	if err := WriteCarrierACL(nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Errorf("file = %q, want empty", b)
	}
}

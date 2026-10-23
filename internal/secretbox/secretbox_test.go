package secretbox

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, KeySize) }

func mustNew(t *testing.T, key []byte) *Box {
	t.Helper()
	b, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := mustNew(t, testKey(1))
	sealed := b.Seal("carrier-pw")
	if !IsSealed(sealed) || !strings.HasPrefix(sealed, Prefix) {
		t.Fatalf("Seal output %q lacks the %q prefix", sealed, Prefix)
	}
	if strings.Contains(sealed, "carrier-pw") {
		t.Fatal("sealed value contains the plaintext")
	}
	got, err := b.Open(sealed)
	if err != nil || got != "carrier-pw" {
		t.Fatalf("Open = %q, %v; want carrier-pw", got, err)
	}
}

func TestSealIsRandomized(t *testing.T) {
	b := mustNew(t, testKey(1))
	if b.Seal("same") == b.Seal("same") {
		t.Fatal("two Seals of the same plaintext are identical (nonce reuse?)")
	}
}

func TestEmptyStaysEmpty(t *testing.T) {
	b := mustNew(t, testKey(1))
	if got := b.Seal(""); got != "" {
		t.Fatalf("Seal(\"\") = %q, want empty (no credential)", got)
	}
	if got, err := b.Open(""); got != "" || err != nil {
		t.Fatalf("Open(\"\") = %q, %v", got, err)
	}
}

func TestOpenLegacyPlaintextPassesThrough(t *testing.T) {
	b := mustNew(t, testKey(1))
	if IsSealed("legacy-pw") {
		t.Fatal("IsSealed(plaintext) = true")
	}
	got, err := b.Open("legacy-pw")
	if err != nil || got != "legacy-pw" {
		t.Fatalf("Open(legacy) = %q, %v", got, err)
	}
}

func TestOpenDetectsTampering(t *testing.T) {
	b := mustNew(t, testKey(1))
	sealed := b.Seal("carrier-pw")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, Prefix))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01
	if _, err := b.Open(Prefix + base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("Open accepted a tampered ciphertext")
	}
	for _, bad := range []string{Prefix, Prefix + "not base64!", Prefix + "AAAA"} {
		if _, err := b.Open(bad); err == nil {
			t.Errorf("Open(%q) succeeded", bad)
		}
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	sealed := mustNew(t, testKey(1)).Seal("carrier-pw")
	if _, err := mustNew(t, testKey(2)).Open(sealed); err == nil {
		t.Fatal("Open with the wrong key succeeded")
	}
}

func TestNewRejectsBadKeyLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Errorf("New accepted a %d-byte key", n)
		}
	}
}

func TestParseKey(t *testing.T) {
	key := testKey(7)
	for name, s := range map[string]string{
		"hex":    hex.EncodeToString(key),
		"HEX":    strings.ToUpper(hex.EncodeToString(key)),
		"base64": base64.StdEncoding.EncodeToString(key),
		"padded": "  " + hex.EncodeToString(key) + "\n",
	} {
		got, err := ParseKey(s)
		if err != nil || !bytes.Equal(got, key) {
			t.Errorf("%s: ParseKey = %x, %v", name, got, err)
		}
	}
	for _, s := range []string{
		"",
		"tring_dev_change_me",
		hex.EncodeToString(key)[:62],                // 31 bytes of hex
		base64.StdEncoding.EncodeToString(key[:16]), // 16 bytes
		strings.Repeat("z", 64),                     // 64 chars, not hex
	} {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("ParseKey(%q) succeeded", s)
		}
	}
}

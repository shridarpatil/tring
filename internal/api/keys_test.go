package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGenerateKeyShape(t *testing.T) {
	for _, env := range []string{"live", "test"} {
		plain, hash, prefix, err := GenerateKey(env)
		if err != nil {
			t.Fatalf("GenerateKey(%q): %v", env, err)
		}
		if !strings.HasPrefix(plain, "tk_"+env+"_") {
			t.Errorf("plain %q missing tk_%s_ prefix", plain, env)
		}
		if prefix != plain[:12] {
			t.Errorf("prefix %q != first 12 chars of %q", prefix, plain)
		}
		if hash != HashKey(plain) {
			t.Errorf("returned hash != HashKey(plain)")
		}
		if len(hash) != 64 {
			t.Errorf("hash len = %d, want 64 (sha256 hex)", len(hash))
		}
	}
}

func TestGenerateKeyUnique(t *testing.T) {
	a, _, _, _ := GenerateKey("live")
	b, _, _, _ := GenerateKey("live")
	if a == b {
		t.Errorf("two GenerateKey calls produced same plaintext %q", a)
	}
}

func TestHashKeyIsDeterministicSHA256(t *testing.T) {
	in := "tk_live_abcdef"
	want := sha256.Sum256([]byte(in))
	if got := HashKey(in); got != hex.EncodeToString(want[:]) {
		t.Errorf("HashKey(%q) = %s, not SHA-256 hex", in, got)
	}
}

func TestGenerateSipPasswordNonEmptyAndRandom(t *testing.T) {
	a, err := GenerateSipPassword()
	if err != nil {
		t.Fatalf("GenerateSipPassword: %v", err)
	}
	if a == "" {
		t.Fatal("password is empty")
	}
	b, _ := GenerateSipPassword()
	if a == b {
		t.Error("two passwords identical — RNG not engaged?")
	}
}

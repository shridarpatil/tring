package webhook

import (
	"bytes"
	"testing"

	"github.com/shridarpatil/tring/internal/secretbox"
)

// Headers are stored sealed (or as plaintext JSON with no key configured);
// a sealed value can't be read without the key.
func TestOpenHeaders(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{3}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed := box.Seal(`{"Authorization":"Bearer x"}`)

	if h, err := New(nil).WithSecrets(box).openHeaders(sealed); err != nil || h["Authorization"] != "Bearer x" {
		t.Fatalf("sealed with key: %v, %v", h, err)
	}
	if _, err := New(nil).openHeaders(sealed); err == nil {
		t.Fatal("sealed without a key: want an error")
	}
	if h, err := New(nil).openHeaders(`{"X-Key":"k"}`); err != nil || h["X-Key"] != "k" {
		t.Fatalf("plaintext: %v, %v", h, err)
	}
	if h, err := New(nil).openHeaders(""); err != nil || h != nil {
		t.Fatalf("none: %v, %v", h, err)
	}
}

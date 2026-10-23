// Package secretbox encrypts credentials Tring must store in a usable form
// (SIP trunk and agent passwords — FreeSWITCH needs the plaintext, so they
// can't be hashed) with AES-256-GCM under the operator's TRING_SECRETS_KEY.
//
// Stored format: "enc:v1:" + base64(nonce || ciphertext+tag), with a fresh
// random 12-byte nonce per Seal. A value without the prefix is legacy
// plaintext: Open returns it unchanged, so rows written before encryption
// was enabled keep working until the api's startup pass seals them.
//
// Losing the key makes every sealed value unrecoverable (operators re-enter
// the trunk passwords); key rotation is not supported yet.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// Prefix marks a sealed value; the version lets a future format coexist.
const Prefix = "enc:v1:"

// Box seals and opens values under one key. Safe for concurrent use.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secretbox: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// ParseKey decodes TRING_SECRETS_KEY: 64 hex characters, or standard
// base64, of exactly 32 bytes. Surrounding whitespace is ignored.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == 2*KeySize {
		if k, err := hex.DecodeString(s); err == nil {
			return k, nil
		}
	}
	if k, err := base64.StdEncoding.DecodeString(s); err == nil && len(k) == KeySize {
		return k, nil
	}
	return nil, fmt.Errorf("secrets key must be 32 bytes: 64 hex characters (generate with: openssl rand -hex 32) or base64 of 32 bytes; got %d characters", len(s))
}

// IsSealed reports whether v is a sealed value (vs legacy plaintext).
func IsSealed(v string) bool { return strings.HasPrefix(v, Prefix) }

// Seal encrypts plaintext. The empty string stays empty: it means "no
// credential set" and callers test for it.
func (b *Box) Seal(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce) // crypto/rand.Read never fails (Go 1.24+)
	out := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return Prefix + base64.StdEncoding.EncodeToString(out)
}

// Open decrypts a sealed value. A value without the prefix is legacy
// plaintext and is returned as-is. A sealed value that fails to decrypt
// (wrong key, tampering, truncation) returns an error.
func (b *Box) Open(stored string) (string, error) {
	if !IsSealed(stored) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil {
		return "", fmt.Errorf("secretbox: malformed sealed value: %w", err)
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns+b.aead.Overhead() {
		return "", errors.New("secretbox: sealed value too short")
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("secretbox: decrypt failed (wrong TRING_SECRETS_KEY or corrupted value)")
	}
	return string(pt), nil
}

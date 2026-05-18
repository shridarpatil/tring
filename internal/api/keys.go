package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// GenerateKey produces a fresh API key, its SHA-256 hash (hex), and the first
// 12 chars of the plaintext for display (Stripe pattern). env is "live" or
// "test".
func GenerateKey(env string) (plaintext, hash, prefix string, err error) {
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	body := strings.ToLower(
		base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b),
	)
	plaintext = fmt.Sprintf("tk_%s_%s", env, body)
	hash = HashKey(plaintext)
	prefix = plaintext[:12]
	return
}

// HashKey returns SHA-256(plaintext) hex-encoded. Used at auth time to look
// up the tenant by the stored hash rather than the plaintext key.
func HashKey(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// GenerateSipPassword produces a random 24-char alphanumeric password for an
// agent's SIP registration. v1 will replace this with short-lived JWTs via
// mod_xml_curl; v0 stores plaintext in the agents table.
func GenerateSipPassword() (string, error) {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(
		base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b),
	), nil
}

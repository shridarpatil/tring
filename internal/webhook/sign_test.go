package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// Receivers verify by recomputing v1 = hex(hmac_sha256(secret, "<ts>.<body>")).
// This test pins the format so a refactor of sign() can't silently break every
// integrator's signature check.
func TestSignMatchesStripeFormat(t *testing.T) {
	secret := "whsec_test"
	ts := int64(1700000000)
	body := []byte(`{"event":"call.answered"}`)

	got := sign(secret, ts, body)

	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Errorf("sign mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestSignDifferentSecretsDifferentSigs(t *testing.T) {
	ts := int64(1700000000)
	body := []byte(`{}`)
	a := sign("secret-a", ts, body)
	b := sign("secret-b", ts, body)
	if a == b {
		t.Error("different secrets produced identical signature")
	}
}

func TestSignDifferentBodiesDifferentSigs(t *testing.T) {
	secret := "whsec_test"
	ts := int64(1700000000)
	a := sign(secret, ts, []byte(`{"a":1}`))
	b := sign(secret, ts, []byte(`{"a":2}`))
	if a == b {
		t.Error("different bodies produced identical signature")
	}
}

func TestSignTimestampBoundToSig(t *testing.T) {
	// Replay protection requires `t` to be part of the signed material —
	// otherwise an attacker could replay the same body with any timestamp.
	secret := "whsec_test"
	body := []byte(`{}`)
	if sign(secret, 1, body) == sign(secret, 2, body) {
		t.Error("timestamp not included in signed material — replay possible")
	}
}

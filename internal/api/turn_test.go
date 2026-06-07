package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

// turnCredential must produce coturn REST-API creds: username "<expiry>[:userid]"
// and credential = base64(HMAC-SHA1(secret, username)) — exactly what coturn
// recomputes with the shared secret.
func TestTurnCredential(t *testing.T) {
	const secret = "topsecret"
	user, cred := turnCredential(secret, "alice", time.Hour)

	exp, rest, ok := strings.Cut(user, ":")
	if !ok || rest != "alice" {
		t.Fatalf("username = %q, want <expiry>:alice", user)
	}
	ts, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		t.Fatalf("expiry not an int: %v", err)
	}
	if want := time.Now().Add(time.Hour).Unix(); ts < want-5 || ts > want+5 {
		t.Fatalf("expiry = %d, want ~%d (now+ttl)", ts, want)
	}

	// Recompute the HMAC the way coturn would and confirm it matches.
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(user))
	if want := base64.StdEncoding.EncodeToString(mac.Sum(nil)); cred != want {
		t.Fatalf("credential = %q, want %q", cred, want)
	}

	// No userid → username is just the expiry timestamp.
	if u, _ := turnCredential(secret, "", time.Hour); strings.Contains(u, ":") {
		t.Fatalf("username with no userid should be the bare expiry, got %q", u)
	}
}

func TestICEServersFor(t *testing.T) {
	static := []iceServer{{URLs: "stun:stun.example:3478"}}

	// No secret → static list returned unchanged.
	s := &Server{iceServers: static}
	if got := s.iceServersFor("alice", time.Hour); len(got) != 1 {
		t.Fatalf("no secret: got %d ICE servers, want 1 (static only)", len(got))
	}

	// Secret set → a fresh ephemeral TURN entry is appended.
	s = &Server{iceServers: static, turnURL: "turn:relay.example:3478", turnSecret: "topsecret"}
	got := s.iceServersFor("alice", time.Hour)
	if len(got) != 2 {
		t.Fatalf("with secret: got %d, want 2 (stun + ephemeral turn)", len(got))
	}
	if turn := got[1]; turn.URLs != "turn:relay.example:3478" || turn.Username == "" || turn.Credential == "" {
		t.Fatalf("ephemeral TURN entry malformed: %+v", turn)
	}
}

// Package turn builds ephemeral coturn REST-API credentials. Both the API
// (credentials handed to browser clients) and the media gateway (its own relay
// candidate) mint from the same shared TURN secret, so the HMAC scheme is
// defined here once and the two can't drift.
package turn

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"time"
)

// Credential returns ephemeral coturn REST-API credentials so the long-lived
// TURN secret never leaves the server:
//
//	username   = "<expiry-unix>[:userid]"
//	credential = base64( HMAC-SHA1(secret, username) )
//
// coturn — configured with `use-auth-secret` + `static-auth-secret=<secret>` —
// recomputes the HMAC and checks the expiry, so a captured/decompiled
// credential is useless after it expires and can't be shared.
//
// ttl MUST comfortably exceed the longest call: the credential authenticates
// the TURN relay ALLOCATION (and its refreshes) for the whole call, so a
// too-short ttl would drop media mid-call. userid (e.g. a client_ref, agent id,
// or "gateway") is folded into the username for traceability in coturn's logs.
func Credential(secret, userid string, ttl time.Duration) (username, credential string) {
	username = strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	if userid != "" {
		username += ":" + userid
	}
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

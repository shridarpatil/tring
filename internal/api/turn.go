package api

import (
	"time"

	"github.com/whatomate/tring/internal/turn"
)

// turnCredential returns ephemeral coturn REST-API credentials. Thin wrapper
// over turn.Credential (shared with the media gateway) so the HMAC scheme lives
// in one place; see that package for the credential format and the coturn
// config it requires (`use-auth-secret` + `static-auth-secret`).
func turnCredential(secret, userid string, ttl time.Duration) (username, credential string) {
	return turn.Credential(secret, userid, ttl)
}

package signaling

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Mirrors the API/gateway token scheme (internal/api/jwt.go, internal/gateway/
// auth.go): the same HS256 agent SDK token authenticates the /rtc socket, so no
// DB lookup is needed — tenant + identity ride in the claims.
const (
	jwtIssuer     = "tring"
	agentAudience = "tring.sdk"
)

// claims are the fields the signaling layer needs from an agent token.
type claims struct {
	TenantID    uuid.UUID `json:"tenant"`
	SipUsername string    `json:"sip_user"`
	jwt.RegisteredClaims
}

// validateToken verifies an HS256 agent token against the shared secret and
// returns its claims.
func validateToken(raw string, secret []byte) (*claims, error) {
	var c claims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	},
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(agentAudience),
		jwt.WithValidMethods([]string{"HS256"}),
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

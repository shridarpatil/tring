package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// These mirror the API's agent-token scheme (internal/api/jwt.go) so the
// gateway validates the SAME token the SDK already obtains — same secret,
// issuer, and audience. No DB lookup needed: the tenant/identity ride in the
// claims.
const (
	jwtIssuer   = "tring"
	jwtAudience = "tring.sdk"
)

// Claims are the agent-token claims the gateway cares about.
type Claims struct {
	TenantID    uuid.UUID `json:"tenant"`
	SipUsername string    `json:"sip_user"`
	SipDomain   string    `json:"sip_domain"`
	jwt.RegisteredClaims
}

// validateToken verifies an HS256 agent token against the shared secret.
func validateToken(raw string, secret []byte) (*Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	},
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(jwtAudience),
		jwt.WithValidMethods([]string{"HS256"}),
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

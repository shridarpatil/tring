package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// These mirror the API's token schemes (internal/api/jwt.go) so the gateway
// validates the SAME tokens the SDK/mint already issue — same secret, issuer,
// and audience. No DB lookup needed: tenant/identity/flow ride in the claims.
const (
	jwtIssuer = "tring"
	// agentAudience scopes the agent SDK token (presented on /agent/ws).
	agentAudience = "tring.sdk"
	// gatewayAudience scopes a visitor's gateway call token (presented on
	// /webrtc/offer): it pins the IVR flow and may carry a trusted client_ref.
	gatewayAudience = "tring.gateway"
)

// Claims are the agent-token claims the gateway cares about (/agent/ws).
type Claims struct {
	TenantID    uuid.UUID `json:"tenant"`
	SipUsername string    `json:"sip_user"`
	SipDomain   string    `json:"sip_domain"`
	jwt.RegisteredClaims
}

// GatewayCallClaims are carried in a visitor's gateway call token (mirrors
// internal/api.GatewayCallClaims): the pinned flow + an optional client_ref.
type GatewayCallClaims struct {
	TenantID  uuid.UUID `json:"tenant"`
	FlowID    string    `json:"flow_id"`
	ClientRef string    `json:"client_ref,omitempty"`
	jwt.RegisteredClaims
}

// validateToken verifies an HS256 agent token against the shared secret.
func validateToken(raw string, secret []byte) (*Claims, error) {
	var c Claims
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

// validateGatewayCallToken verifies a visitor's gateway call token.
func validateGatewayCallToken(raw string, secret []byte) (*GatewayCallClaims, error) {
	var c GatewayCallClaims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	},
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(gatewayAudience),
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

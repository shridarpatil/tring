package signaling

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Mirrors the API/gateway token scheme (internal/api/jwt.go, internal/gateway/
// auth.go): the same HS256 tokens authenticate the /rtc socket, so no DB lookup
// is needed — tenant + identity ride in the claims. /rtc accepts TWO audiences:
//   - tring.sdk     — an agent (presence + accepting calls); carries sip_user.
//   - tring.gateway — a visitor (web caller); carries a server-pinned flow_id,
//     an optional client_ref, and a jti the edge burns single-use (anti-replay).
const (
	jwtIssuer       = "tring"
	agentAudience   = "tring.sdk"
	gatewayAudience = "tring.gateway"
)

// claims are the fields the signaling layer needs from an agent token.
type claims struct {
	TenantID    uuid.UUID `json:"tenant"`
	SipUsername string    `json:"sip_user"`
	jwt.RegisteredClaims
}

// gatewayCallClaims are carried in a visitor's gateway call token (mirrors
// internal/api.GatewayCallClaims / internal/gateway.GatewayCallClaims).
type gatewayCallClaims struct {
	TenantID  uuid.UUID `json:"tenant"`
	FlowID    string    `json:"flow_id"`
	ClientRef string    `json:"client_ref,omitempty"`
	jwt.RegisteredClaims
}

// principal is the authenticated identity on /rtc — an agent or a visitor.
type principal struct {
	tenant    uuid.UUID
	sip       string // agent: their SIP identity
	isVisitor bool
	flowID    string    // visitor: the server-pinned IVR flow
	clientRef string    // visitor: trusted caller ref → CDR
	jti       string    // visitor: single-use id (burned by the edge)
	exp       time.Time // visitor: jti TTL
}

func hmacKey(secret []byte) jwt.Keyfunc {
	return func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	}
}

// validateToken verifies an HS256 agent token and returns its claims.
func validateToken(raw string, secret []byte) (*claims, error) {
	var c claims
	_, err := jwt.ParseWithClaims(raw, &c, hmacKey(secret),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(agentAudience),
		jwt.WithValidMethods([]string{"HS256"}),
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// validatePrincipal accepts either an agent token (tring.sdk) or a visitor
// gateway call token (tring.gateway) and returns a unified principal.
func validatePrincipal(raw string, secret []byte) (*principal, error) {
	if c, err := validateToken(raw, secret); err == nil {
		return &principal{tenant: c.TenantID, sip: c.SipUsername}, nil
	}
	var c gatewayCallClaims
	if _, err := jwt.ParseWithClaims(raw, &c, hmacKey(secret),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(gatewayAudience),
		jwt.WithValidMethods([]string{"HS256"}),
	); err != nil {
		return nil, err
	}
	p := &principal{tenant: c.TenantID, isVisitor: true, flowID: c.FlowID, clientRef: c.ClientRef, jti: c.ID}
	if c.ExpiresAt != nil {
		p.exp = c.ExpiresAt.Time
	}
	return p, nil
}

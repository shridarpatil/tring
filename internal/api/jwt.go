package api

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	jwtIssuer   = "tring"
	jwtAudience = "tring.sdk"
)

// AgentClaims are the JWT claims carried in tokens issued for the JS SDK.
// The token's `sub` is the agent UUID; `tenant`, `sip_user`, and `sip_domain`
// are denormalized so the SDK and downstream handlers don't need a DB lookup
// to know which tenant/identity is acting.
type AgentClaims struct {
	TenantID    uuid.UUID `json:"tenant"`
	SipUsername string    `json:"sip_user"`
	SipDomain   string    `json:"sip_domain"`
	jwt.RegisteredClaims
}

func (s *Server) IssueAgentToken(agentID, tenantID uuid.UUID, sipUser, sipDomain string, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(ttl)
	claims := AgentClaims{
		TenantID:    tenantID,
		SipUsername: sipUser,
		SipDomain:   sipDomain,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   agentID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    jwtIssuer,
			Audience:  []string{jwtAudience},
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.jwtSecret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

func (s *Server) ParseAgentToken(raw string) (*AgentClaims, error) {
	var c AgentClaims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
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

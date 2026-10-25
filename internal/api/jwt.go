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
	// gatewayAudience scopes a short-lived "gateway call token" — minted for a
	// visitor web call, validated by the media gateway. It carries the pinned
	// flow and (for the trusted mint) a server-verified client_ref the gateway
	// propagates to the call's CDR. Distinct audience from the agent SDK token
	// so the two can't be used interchangeably.
	gatewayAudience = "tring.gateway"
	// supervisorAudience scopes a short-lived RTC token for a console operator to
	// join a live web call in monitor/whisper/barge. Single-use jti (burned by the
	// gateway), room-pinned server-side. Distinct audience from every other token.
	supervisorAudience = "tring.supervisor"
	// controlAudience scopes the API's own short-lived token for the gateway's
	// loopback control endpoints (POST /control/connect). Minted per request;
	// never handed to a client.
	controlAudience = "tring.control"
)

// GatewayCallClaims are carried in a visitor's gateway call token. The token
// pins the IVR flow server-side (a browser can't widen its own routing) and,
// when minted by the trusted API-key endpoint, attributes a client_ref.
type GatewayCallClaims struct {
	TenantID  uuid.UUID `json:"tenant"`
	FlowID    string    `json:"flow_id"`
	ClientRef string    `json:"client_ref,omitempty"`
	jwt.RegisteredClaims
}

// IssueGatewayCallToken mints a short-lived token a visitor's browser presents
// to the media gateway. flowID is required (the call is hard-pinned to it);
// clientRef is empty for anonymous callers and a verified id for the trusted
// mint.
func (s *Server) IssueGatewayCallToken(tenantID uuid.UUID, flowID, clientRef string, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(ttl)
	claims := GatewayCallClaims{
		TenantID:  tenantID,
		FlowID:    flowID,
		ClientRef: clientRef,
		RegisteredClaims: jwt.RegisteredClaims{
			// Unique jti so the gateway can enforce single-use (consume on first
			// /webrtc/offer), defeating replay of a captured token within its TTL.
			ID:        uuid.NewString(),
			Subject:   tenantID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    jwtIssuer,
			Audience:  []string{gatewayAudience},
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.jwtSecret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// SupervisorClaims are carried in a supervisor RTC token. Room is the pinned
// pion room NAME (un-namespaced; the gateway prefixes the tenant). Name is the
// operator's display name, shown to the supervised agent.
type SupervisorClaims struct {
	TenantID uuid.UUID `json:"tenant"`
	Room     string    `json:"room"`
	Name     string    `json:"name"`
	jwt.RegisteredClaims
}

// IssueSupervisorToken mints a short-lived, single-use token a console
// supervisor's browser presents to the media gateway to join a specific live
// room. userID becomes the JWT subject (audit); ttl should be short (~2m).
func (s *Server) IssueSupervisorToken(tenantID uuid.UUID, roomName, userID, name string, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(ttl)
	claims := SupervisorClaims{
		TenantID: tenantID,
		Room:     roomName,
		Name:     name,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    jwtIssuer,
			Audience:  []string{supervisorAudience},
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.jwtSecret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// ControlClaims are carried in the API's token for the gateway's control
// endpoints: just the tenant the call is placed for.
type ControlClaims struct {
	TenantID uuid.UUID `json:"tenant"`
	jwt.RegisteredClaims
}

// issueControlToken mints the token for one control call to the gateway.
func (s *Server) issueControlToken(tenantID uuid.UUID) (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, ControlClaims{
		TenantID: tenantID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)),
			Issuer:    jwtIssuer,
			Audience:  []string{controlAudience},
		},
	}).SignedString(s.jwtSecret)
}

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

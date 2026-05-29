package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Console session TTL. Long enough that operators don't get punted out
// during a workday, short enough that a stolen JWT has a bounded blast
// radius. Refresh is "log in again" — no refresh tokens in v0.
const consoleTokenTTL = 12 * time.Hour

const (
	consoleJWTIssuer   = "tring"
	consoleJWTAudience = "tring.console"
)

// ConsoleClaims are the JWT claims for a logged-in console user.
// Subject is the user UUID; TenantID is denormalized to avoid a lookup
// on every authenticated request.
type ConsoleClaims struct {
	TenantID uuid.UUID `json:"tenant"`
	Email    string    `json:"email"`
	jwt.RegisteredClaims
}

type consoleLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type consoleLoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Tenant    Tenant    `json:"tenant"`
}

// handleConsoleLogin authenticates an email+password against the users
// table and returns a JWT the Vue console uses for all subsequent calls.
// Public endpoint (no auth wall).
func (s *Server) handleConsoleLogin(w http.ResponseWriter, r *http.Request) {
	var req consoleLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required")
		return
	}

	var (
		userID       uuid.UUID
		tenantID     uuid.UUID
		passwordHash string
	)
	err := s.db.Pool.QueryRow(r.Context(),
		`SELECT id, tenant_id, password_hash FROM users WHERE email = $1`,
		email,
	).Scan(&userID, &tenantID, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Generic message to avoid leaking which half is wrong (account
		// enumeration). bcrypt comparison still runs against a fake hash
		// below to keep timing roughly constant.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidi"), []byte(req.Password))
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Pull the tenant in the same shape AuthRequired loads it so the
	// console gets a consistent /v1/tenants/me-equivalent payload back
	// from /login without a second round-trip.
	var t Tenant
	err = s.db.Pool.QueryRow(r.Context(),
		`SELECT id, name, sip_domain, outbound_strategy, failover_causes
		 FROM tenants WHERE id = $1`,
		tenantID,
	).Scan(&t.ID, &t.Name, &t.SipDomain, &t.OutboundStrategy, &t.FailoverCauses)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load tenant")
		return
	}

	now := time.Now()
	exp := now.Add(consoleTokenTTL)
	claims := ConsoleClaims{
		TenantID: tenantID,
		Email:    email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    consoleJWTIssuer,
			Audience:  []string{consoleJWTAudience},
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue token")
		return
	}

	writeJSON(w, http.StatusOK, consoleLoginResponse{
		Token:     signed,
		ExpiresAt: exp,
		Tenant:    t,
	})
}

// parseConsoleToken validates a Bearer JWT issued by handleConsoleLogin
// and returns the tenant_id. Used by AuthRequired's dual-mode path.
func (s *Server) parseConsoleToken(raw string) (*ConsoleClaims, error) {
	var c ConsoleClaims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	},
		jwt.WithIssuer(consoleJWTIssuer),
		jwt.WithAudience(consoleJWTAudience),
		jwt.WithValidMethods([]string{"HS256"}),
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

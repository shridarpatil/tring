package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	// Role is a UI hint only (which console to render); authorization always
	// reloads the role from the DB (loadPrincipal).
	Role string `json:"role,omitempty"`
	// Epoch is users.session_epoch at issue time; a credential change bumps
	// the row's epoch, which revokes this token (loadPrincipal).
	Epoch int `json:"ep"`
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
	User      meUser    `json:"user"`
}

// meUser is the logged-in console user as the console sees it: login returns
// it so the console can pick its view without a second round-trip.
type meUser struct {
	ID          string      `json:"id"`
	DisplayName string      `json:"display_name"`
	Email       string      `json:"email"`
	Role        string      `json:"role"`
	SipUsername string      `json:"sip_username"`
	Teams       []myTeamRef `json:"teams"`
}

type myTeamRef struct {
	Name      string `json:"name"`
	IsManager bool   `json:"is_manager"`
}

func newMeUser(p *Principal, email string) meUser {
	// Non-nil so a user in no team serialises as [] rather than null.
	teams := make([]myTeamRef, 0, len(p.Teams))
	for _, name := range p.Teams {
		teams = append(teams, myTeamRef{Name: name, IsManager: slices.Contains(p.ManagedTeams, name)})
	}
	return meUser{
		ID:          p.UserID.String(),
		DisplayName: p.DisplayName,
		Email:       email,
		Role:        p.Role,
		SipUsername: p.SipUsername,
		Teams:       teams,
	}
}

// handleConsoleLogin authenticates an email+password against the users
// table and returns a JWT the Vue console uses for all subsequent calls.
// Public endpoint (no auth wall), so it's throttled per client IP and per
// email (loginAttemptLimit per loginAttemptWindow each) → 429 + Retry-After.
func (s *Server) handleConsoleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry, ok := s.loginByIP.take(ip); !ok {
		writeTooManyRequests(w, retry)
		return
	}
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
	if retry, ok := s.loginByEmail.take(email); !ok {
		writeTooManyRequests(w, retry)
		return
	}

	var (
		userID       uuid.UUID
		tenantID     uuid.UUID
		passwordHash *string
	)
	err := s.db.Pool.QueryRow(r.Context(), s.q.GetUserByEmail,
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

	if passwordHash == nil {
		// SSO-only user: no password to check. Burn the same bcrypt time and
		// answer exactly like a wrong password so this can't be told apart.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidi"), []byte(req.Password))
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*passwordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// Correct password: refund the attempt so legitimate logins (many users
	// behind one office NAT) don't eat into the guessing budget.
	s.loginByIP.release(ip)
	s.loginByEmail.release(email)
	// Every login began with login/start from this IP; refund that too so a
	// NAT'd office of password users can't exhaust the SSO users' budget.
	s.ssoStartByIP.release(ip)

	// Checked only after a correct password, so it reveals nothing to a guesser.
	blocked, err := s.passwordLoginBlocked(r.Context(), userID, tenantID, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if blocked {
		writeError(w, http.StatusForbidden, "this account must sign in with SSO")
		return
	}

	resp, err := s.consoleSession(r.Context(), userID, tenantID, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// passwordLoginBlocked reports whether the user's email domain belongs to an
// enabled require_sso provider in the user's own tenant. Admins are exempt:
// they are the break-glass accounts when an IdP is broken. Errors fail closed
// (the caller refuses the login).
func (s *Server) passwordLoginBlocked(ctx context.Context, userID, tenantID uuid.UUID, email string) (bool, error) {
	domain := emailDomain(email)
	if domain == "" {
		return false, nil
	}
	var (
		pid, tid                 uuid.UUID
		issuer, clientID, secret string
		requireSSO               bool
	)
	err := s.db.Pool.QueryRow(ctx, s.q.GetSSODomain, domain).Scan(&pid, &tid, &issuer, &clientID, &secret, &requireSSO)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if tid != tenantID || !requireSSO {
		return false, nil
	}
	// Only the rare enforced-domain login pays for this extra principal load.
	p, err := s.principalFor(ctx, userID, tenantID)
	if err != nil {
		return false, err
	}
	return p.Role != "admin", nil
}

// consoleSession builds the login response (tenant, user view, signed
// console JWT) for an already-authenticated user. Shared by password login
// and the SSO callback so both issue identical sessions. Errors are short
// messages safe to show the client.
func (s *Server) consoleSession(ctx context.Context, userID, tenantID uuid.UUID, email string) (consoleLoginResponse, error) {
	// Pull the tenant in the same shape AuthRequired loads it so the
	// console gets a consistent /v1/tenants/me-equivalent payload back
	// from /login without a second round-trip.
	var t Tenant
	err := s.db.Pool.QueryRow(ctx, s.q.GetTenant,
		tenantID,
	).Scan(&t.ID, &t.Name, &t.SipDomain, &t.OutboundStrategy, &t.FailoverCauses)
	if err != nil {
		return consoleLoginResponse{}, errors.New("load tenant")
	}

	principal, err := s.principalFor(ctx, userID, tenantID)
	if err != nil {
		return consoleLoginResponse{}, errors.New("load user")
	}

	signed, exp, err := s.IssueConsoleToken(userID, tenantID, email, principal.Role, principal.Epoch, consoleTokenTTL)
	if err != nil {
		return consoleLoginResponse{}, errors.New("issue token")
	}

	return consoleLoginResponse{
		Token:     signed,
		ExpiresAt: exp,
		Tenant:    t,
		User:      newMeUser(principal, email),
	}, nil
}

// IssueConsoleToken signs a console-audience JWT. Exported so tests can
// mint authenticated requests without going through /v1/console/login.
func (s *Server) IssueConsoleToken(userID, tenantID uuid.UUID, email, role string, epoch int, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(ttl)
	claims := ConsoleClaims{
		TenantID: tenantID,
		Email:    email,
		Role:     role,
		Epoch:    epoch,
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
		return "", time.Time{}, err
	}
	return signed, exp, nil
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

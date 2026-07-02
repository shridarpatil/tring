package api

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestIssueSupervisorToken(t *testing.T) {
	s := &Server{jwtSecret: []byte("k")}
	tenant := uuid.New()
	raw, exp, err := s.IssueSupervisorToken(tenant, "room-x", "user-9", "Sam", 2*time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if time.Until(exp) <= 0 {
		t.Fatal("exp must be in the future")
	}
	var c SupervisorClaims
	_, err = jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return s.jwtSecret, nil },
		jwt.WithIssuer(jwtIssuer), jwt.WithAudience(supervisorAudience), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.TenantID != tenant || c.Room != "room-x" || c.Name != "Sam" || c.Subject != "user-9" || c.ID == "" {
		t.Fatalf("bad claims: %+v", c)
	}
}

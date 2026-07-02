package signaling

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func mintSupervisor(t *testing.T, secret []byte, tenant uuid.UUID, room, jti string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"tenant": tenant.String(),
		"room":   room,
		"name":   "Sam Super",
		"jti":    jti,
		"sub":    "user-1",
		"iss":    "tring",
		"aud":    "tring.supervisor",
		"exp":    time.Now().Add(2 * time.Minute).Unix(),
		"iat":    time.Now().Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

func TestValidatePrincipalSupervisor(t *testing.T) {
	secret := []byte("test-secret")
	tenant := uuid.New()
	raw := mintSupervisor(t, secret, tenant, "abc-room", "jti-1")

	p, err := validatePrincipal(raw, secret)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !p.isSupervisor {
		t.Fatal("expected isSupervisor")
	}
	if p.isVisitor {
		t.Fatal("supervisor must not be a visitor")
	}
	if p.tenant != tenant || p.room != "abc-room" || p.name != "Sam Super" || p.jti != "jti-1" {
		t.Fatalf("unexpected principal: %+v", p)
	}
}

func TestValidatePrincipalRejectsWrongSecret(t *testing.T) {
	raw := mintSupervisor(t, []byte("secret-a"), uuid.New(), "r", "j")
	if _, err := validatePrincipal(raw, []byte("secret-b")); err == nil {
		t.Fatal("expected rejection on wrong secret")
	}
}

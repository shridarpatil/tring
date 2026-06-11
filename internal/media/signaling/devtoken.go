package signaling

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// devTenant is the fixed tenant the dev-token mint issues for, so two browser
// tabs minting separately land in the SAME tenant (and thus can share a room).
var devTenant = uuid.MustParse("00000000-0000-0000-0000-0000000000de")

// handleDevToken mints a short-lived agent JWT for the local test client
// (examples/rtc-test.html). It is mounted ONLY when rtc_dev_tokens is true and
// must never be enabled in production — it issues tokens with no authentication.
// This exists so the media plane is demoable with `make gateway` alone, without
// standing up the API + DB to mint a real agent token.
func (s *Server) handleDevToken(w http.ResponseWriter, r *http.Request) {
	sip := r.URL.Query().Get("name")
	if sip == "" {
		sip = "dev"
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant":   devTenant.String(),
		"sip_user": sip,
		"iss":      jwtIssuer,
		"aud":      agentAudience,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := tok.SignedString(s.cfg.JWTSecret)
	if err != nil {
		http.Error(w, `{"error":"sign failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": signed})
}

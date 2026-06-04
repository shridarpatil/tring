package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// jtiStore consumes one-time token ids. *cache.Cache (Redis) satisfies it via
// SetNX; tests fake it. Returns true on the FIRST use of a key, false on a
// replay. Fail-open: a nil/erroring store returns true (a cache outage must
// never block legitimate calls — the concurrency cap is the other guard).
type jtiStore interface {
	SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) bool
}

// consumeToken enforces single-use of a gateway call token: true the FIRST time
// a jti is seen, false on any replay within the token's remaining TTL. An empty
// jti, a nil store, or an already-expired token are allowed (the JWT exp check
// and the concurrency cap are the other guards).
func (g *Gateway) consumeToken(ctx context.Context, jti string, exp time.Time) bool {
	if jti == "" || g.tokens == nil {
		return true
	}
	ttl := time.Until(exp)
	if ttl <= 0 {
		return true
	}
	return g.tokens.SetNX(ctx, "gwtoken:"+jti, []byte("1"), ttl)
}

type offerRequest struct {
	SDP string `json:"sdp"`
	// FlowID pins this call to an IVR flow (overrides the gateway's default).
	// The SDK supplies it so the flow is chosen per call, not per deployment.
	FlowID string `json:"flow_id"`
}

type answerResponse struct {
	SDP string `json:"sdp"`
}

// Handler is the gateway's HTTP surface: WebRTC offer/answer signaling (and a
// health check). Signaling is plain HTTPS; the visitor offer optionally carries
// a gateway call token and the agent WS always validates the agent JWT. CORS is
// on so the SDK on another origin can post offers. No HTML is served from the
// gateway itself.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/webrtc/offer", g.handleOffer)
	// Agent presence + incoming-call signaling WebSocket (M4b). Agents hold
	// this open while online; FS-delivered agent legs ring the browser over it.
	mux.HandleFunc("/agent/ws", g.handleAgentWS)
	// Dev-only: headless verification of the FreeSWITCH leg (milestone 2a).
	mux.HandleFunc("/fsleg/test", func(w http.ResponseWriter, r *http.Request) {
		n, err := g.TestFSLeg(r.Context())
		if err != nil {
			g.log.Printf("gateway: fsleg test failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"fs_leg":"ok","inbound_rtp_packets":%d}`, n)))
	})
	return cors(mux)
}

func (g *Gateway) handleOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req offerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SDP == "" {
		http.Error(w, `{"error":"sdp required"}`, http.StatusBadRequest)
		return
	}

	// A gateway call token (from the API's guest mint) pins the flow server-
	// side and may carry a trusted client_ref — both override anything the
	// browser put in the body. Anonymous callers post no token and supply
	// flow_id in the body; RequireAuth makes the token mandatory.
	flowID, clientRef := req.FlowID, ""
	if raw := bearer(r); raw != "" {
		claims, err := validateGatewayCallToken(raw, g.cfg.JWTSecret)
		if err != nil {
			http.Error(w, `{"error":"invalid or expired call token"}`, http.StatusUnauthorized)
			return
		}
		// Single-use: consume the token's jti so a captured token can't be
		// replayed within its TTL to spin up more bridges. See consumeToken.
		var exp time.Time
		if claims.ExpiresAt != nil {
			exp = claims.ExpiresAt.Time
		}
		if !g.consumeToken(r.Context(), claims.ID, exp) {
			http.Error(w, `{"error":"call token already used"}`, http.StatusUnauthorized)
			return
		}
		flowID, clientRef = claims.FlowID, claims.ClientRef
	} else if g.cfg.RequireAuth {
		http.Error(w, `{"error":"call token required"}`, http.StatusUnauthorized)
		return
	}

	// TODO(cap): enforce a per-tenant + global concurrent-bridge cap HERE —
	// before Bridge dials FS / binds a UDP port — so a rejection (503/429)
	// allocates nothing. Key on the tenant claim (VPN/NAT/CDN-proof), in-memory
	// per gateway now, Redis (TTL'd per-call keys) when multi-gateway. This is
	// the backstop that single-use tokens + CAPTCHA do NOT cover: many-token
	// minting by an insider/compromised account, a buggy backend, integrator
	// CAPTCHA misconfig, Redis-outage fail-open, and multi-tenant fairness.

	// Bridge into FreeSWITCH when configured; otherwise standalone echo (M1).
	answer, err := g.Bridge(r.Context(), req.SDP, flowID, clientRef)
	if err != nil {
		g.log.Printf("gateway: offer failed: %v", err)
		http.Error(w, `{"error":"negotiation failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answerResponse{SDP: answer})
}

// cors echoes the request Origin so the browser SDK (cross-origin) can post
// offers. Mirrors the API's permissive dev CORS.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

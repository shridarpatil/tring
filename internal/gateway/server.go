package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// jtiStore consumes one-time token ids. *cache.Cache (Redis) satisfies it via
// SetNX; tests fake it. Returns (true, nil) on the FIRST use of a key, (false,
// nil) on a replay, and (false, err) when the store can't be reached.
type jtiStore interface {
	SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error)
}

// errTokenReused is returned by consumeToken when a jti has already been spent.
// The handler maps it to 401; any other consumeToken error means single-use
// could not be PROVEN and is treated as a hard rejection (fail closed).
var errTokenReused = errors.New("gateway call token already used")

// consumeToken enforces single-use of a gateway call token. It returns nil only
// when the jti is seen for the FIRST time and the call may proceed. It returns
// an error — and the caller MUST reject the offer — on a replay (errTokenReused)
// or when single-use cannot be proven: a token with no jti, or an unreachable
// store. This fails CLOSED on purpose: Redis is a hard dependency, so a captured
// token can't be replayed during a cache outage (the old fail-open behaviour).
// An already-expired token is caught by the JWT exp check before we get here; a
// non-positive remaining TTL is treated as expired and rejected.
func (g *Gateway) consumeToken(ctx context.Context, jti string, exp time.Time) error {
	if jti == "" {
		return errors.New("gateway call token has no jti")
	}
	ttl := time.Until(exp)
	if ttl <= 0 {
		return errors.New("gateway call token expired")
	}
	if g.tokens == nil {
		return errors.New("single-use store unavailable")
	}
	fresh, err := g.tokens.SetNX(ctx, "gwtoken:"+jti, []byte("1"), ttl)
	if err != nil {
		return fmt.Errorf("single-use check failed: %w", err)
	}
	if !fresh {
		return errTokenReused
	}
	return nil
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
		// replayed within its TTL to spin up more bridges. Fails closed — if the
		// store is unreachable we reject (503) rather than risk a replay. See
		// consumeToken.
		var exp time.Time
		if claims.ExpiresAt != nil {
			exp = claims.ExpiresAt.Time
		}
		if err := g.consumeToken(r.Context(), claims.ID, exp); err != nil {
			if errors.Is(err, errTokenReused) {
				http.Error(w, `{"error":"call token already used"}`, http.StatusUnauthorized)
				return
			}
			// Single-use couldn't be proven (store down / malformed token):
			// reject rather than fail open. Retryable once the store recovers.
			g.log.Printf("gateway: single-use rejected: %v", err)
			http.Error(w, `{"error":"call token verification unavailable"}`, http.StatusServiceUnavailable)
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
	// CAPTCHA misconfig, and multi-tenant fairness. (Redis-outage replay is no
	// longer a gap here — single-use now fails closed; see consumeToken.)

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

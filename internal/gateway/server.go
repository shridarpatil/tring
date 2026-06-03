package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type offerRequest struct {
	SDP string `json:"sdp"`
}

type answerResponse struct {
	SDP string `json:"sdp"`
}

// Handler is the gateway's HTTP surface: WebRTC offer/answer signaling (and a
// health check). Signaling is plain HTTPS — auth (agent JWT / API key / guest)
// is layered in milestone 3; for now it's open for the echo POC. CORS is on so
// a browser page on another origin (e.g. examples/gateway-echo.html, the SDK)
// can post offers. No HTML is served from the gateway itself.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/webrtc/offer", g.handleOffer)
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
	if !g.authOffer(w, r) {
		return
	}
	var req offerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SDP == "" {
		http.Error(w, `{"error":"sdp required"}`, http.StatusBadRequest)
		return
	}
	// Bridge into FreeSWITCH when configured; otherwise standalone echo (M1).
	answer, err := g.Bridge(r.Context(), req.SDP)
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

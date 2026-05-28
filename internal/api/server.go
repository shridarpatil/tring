package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/freeswitch"
	"github.com/whatomate/tring/internal/webhook"
	"github.com/whatomate/tring/internal/webui"
)

// Config bundles the dependencies an api.Server needs at construction time.
// Wiring it explicitly (vs. picking env vars inside api/) keeps the package
// testable and lets the CLI in cmd/api drive defaults.
type Config struct {
	DB          *db.DB
	JWTSecret   string
	FSBasicAuth string // "user:pass" — shared with FreeSWITCH mod_xml_curl
	WSURL       string // ws(s)://host:port that the SDK passes to sip.js
	ESLAddr     string // FreeSWITCH ESL inbound, e.g. 127.0.0.1:8021
	ESLPassword string // FreeSWITCH ESL password (default ClueCon)

	// WebRTC ICE servers. STUN gives the browser its srflx (public)
	// candidate. TURN relays media through a public-IP server when both
	// peers are behind symmetric NAT or CGNAT and direct paths fail.
	// All three TURN fields are required together; absent TURN means
	// the SDK gets a STUN-only iceServers list.
	STUNURL      string
	TURNURL      string
	TURNUsername string
	TURNPassword string
}

type Server struct {
	db          *db.DB
	jwtSecret   []byte
	fsBasicUser string
	fsBasicPass string
	wsURL       string
	fs          *freeswitch.Client
	notifier    *webhook.Notifier

	// Resolved iceServers built once at startup from STUN/TURN config and
	// returned verbatim by /v1/sdk/credentials. Empty slice == "no ICE
	// servers configured", which is valid (works on LANs).
	iceServers []iceServer

	// Round-robin trunk selection state. Lazily initialised in
	// nextRoundRobin (see trunks.go).
	rrMu sync.Mutex
	rr   map[uuid.UUID]int
}

// iceServer matches the RTCIceServer JSON shape consumed by browsers.
// `urls` can be a single URL or an array of URLs; we always serialize a
// single string here for simplicity. `credential` only sent for TURN.
type iceServer struct {
	URLs       string `json:"urls"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

func NewServer(cfg Config) *Server {
	user, pass, _ := strings.Cut(cfg.FSBasicAuth, ":")
	return &Server{
		db:          cfg.DB,
		jwtSecret:   []byte(cfg.JWTSecret),
		fsBasicUser: user,
		fsBasicPass: pass,
		wsURL:       cfg.WSURL,
		fs:          freeswitch.New(cfg.ESLAddr, cfg.ESLPassword),
		notifier:    webhook.New(cfg.DB),
		iceServers:  buildICEServers(cfg),
	}
}

// buildICEServers turns the STUN/TURN env config into the iceServers
// array the SDK passes to RTCPeerConnection. Order matters: STUN first
// so the browser tries direct candidates before falling back to TURN
// relay (which costs the operator bandwidth).
func buildICEServers(cfg Config) []iceServer {
	out := []iceServer{}
	if cfg.STUNURL != "" {
		out = append(out, iceServer{URLs: cfg.STUNURL})
	}
	if cfg.TURNURL != "" {
		out = append(out, iceServer{
			URLs:       cfg.TURNURL,
			Username:   cfg.TURNUsername,
			Credential: cfg.TURNPassword,
		})
	}
	return out
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	// CORS must come BEFORE the auth wall — preflight OPTIONS requests
	// from browsers don't carry `Authorization`, so they'd otherwise 401.
	r.Use(corsMiddleware)

	r.Get("/healthz", s.handleHealthz)

	// FreeSWITCH-facing endpoints. Basic Auth shared with mod_xml_curl;
	// not behind the tenant API-key wall.
	r.Route("/fs", func(r chi.Router) {
		r.Use(s.FSBasicAuth)
		r.Post("/directory", s.handleFSDirectory)
	})

	r.Route("/v1", func(r chi.Router) {
		// Tenant-scoped routes: API key auth, full access within the tenant.
		r.Group(func(r chi.Router) {
			r.Use(s.AuthRequired)
			r.Get("/tenants/me", s.handleGetMyTenant)
			r.Patch("/tenants/me", s.handlePatchMyTenant)
			r.Post("/agents", s.handleCreateAgent)
			r.Get("/agents", s.handleListAgents)
			r.Get("/agents/{id}", s.handleGetAgent)
			r.Get("/agents/{id}/token", s.handleAgentToken)
			r.Post("/numbers", s.handleCreateNumber)
			r.Get("/numbers", s.handleListNumbers)
			r.Patch("/numbers/{id}", s.handlePatchNumber)

			r.Post("/calls", s.handleCreateCall)
			r.Post("/calls/connect", s.handleConnectCall)
			r.Get("/calls", s.handleListCalls)
			r.Get("/calls/{id}", s.handleGetCall)

			r.Post("/webhooks", s.handleCreateWebhook)
			r.Get("/webhooks", s.handleListWebhooks)
			r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
			r.Get("/webhooks/{id}/deliveries", s.handleListDeliveries)

			r.Post("/flows", s.handleCreateFlow)
			r.Get("/flows", s.handleListFlows)
			r.Get("/flows/{id}", s.handleGetFlow)
			r.Patch("/flows/{id}", s.handlePatchFlow)
			r.Delete("/flows/{id}", s.handleDeleteFlow)

			r.Post("/trunks", s.handleCreateTrunk)
			r.Get("/trunks", s.handleListTrunks)
			r.Get("/trunks/{id}", s.handleGetTrunk)
			r.Patch("/trunks/{id}", s.handlePatchTrunk)
			r.Delete("/trunks/{id}", s.handleDeleteTrunk)
			r.Post("/trunks/reload", s.handleReloadTrunks)
		})

		// SDK routes: JWT auth, scoped to the JWT's subject agent.
		r.Group(func(r chi.Router) {
			r.Use(s.JWTAuthRequired)
			r.Get("/sdk/credentials", s.handleSDKCredentials)
			r.Post("/sdk/calls", s.handleSDKCreateCall)
		})
	})

	// Admin console (embedded Vue dist). Mounted only if the Vue app was
	// actually built — backend-only deployments get a UI-less binary
	// automatically (no flag needed, just don't run `npm run build`).
	if uiFS, ok := webui.FS(); ok {
		fileServer := http.FileServer(http.FS(uiFS))
		// Strip "/admin" prefix so requests for /admin/assets/foo.js
		// resolve to "assets/foo.js" within the embedded FS.
		stripped := http.StripPrefix("/admin", fileServer)
		// Catch-all: any /admin/* path that doesn't match a static file
		// falls back to index.html so the Vue router (history mode) can
		// own the URL space client-side.
		r.Get("/admin", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/admin/", http.StatusMovedPermanently)
		})
		r.Get("/admin/*", func(w http.ResponseWriter, req *http.Request) {
			// If it's a known asset (has a file extension), serve it
			// directly; otherwise serve index.html for SPA routing.
			path := strings.TrimPrefix(req.URL.Path, "/admin/")
			if path != "" {
				if _, err := uiFS.Open(path); err == nil {
					stripped.ServeHTTP(w, req)
					return
				}
			}
			indexHTML, err := uiFS.Open("index.html")
			if err != nil {
				http.Error(w, "UI not available", http.StatusNotFound)
				return
			}
			defer indexHTML.Close()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.Copy(w, indexHTML)
		})
	}

	return r
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Pool.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// corsMiddleware reflects the request's Origin so browsers from any CRM
// (dev: localhost:9999, prod: customer's CRM domain) can hit the API. The
// browser SDK is the primary cross-origin caller; tenant backends speak
// server-to-server and don't need CORS at all.
//
// v0 is permissive (echo any Origin). Phase 2: per-tenant allowed-origins
// list to lock things down once we're confident in the abuse surface.
// The /fs/* endpoints are deliberately excluded — they're localhost-only
// by design.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key")
			h.Set("Access-Control-Max-Age", "3600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

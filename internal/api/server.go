package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/freeswitch"
	"github.com/whatomate/tring/internal/orchestrator"
	"github.com/whatomate/tring/internal/presence"
	"github.com/whatomate/tring/internal/queries"
	"github.com/whatomate/tring/internal/webhook"
	"github.com/whatomate/tring/internal/webui"
	"github.com/whatomate/tring/internal/widget"
)

// Config bundles the dependencies an api.Server needs at construction time.
// Wiring it explicitly (vs. picking env vars inside api/) keeps the package
// testable and lets the CLI in cmd/api drive defaults.
type Config struct {
	DB          *db.DB
	JWTSecret   string
	FSBasicAuth string // "user:pass" — shared with FreeSWITCH mod_xml_curl
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

	// AgentTransport routes the agent's own call leg: "sip" (agent registers
	// to FS directly) or "gateway" (leg routed to the media gateway as a SIP
	// endpoint; ADR-001 M4b). AgentGateway is the FS gateway name used for the
	// latter. Defaults supplied by internal/config.
	AgentTransport string
	AgentGateway   string

	// GatewayPublicURL is the browser-reachable base URL of the media gateway,
	// returned to visitors in a guest mint response (ADR-001 M5).
	GatewayPublicURL string

	// AudioDir is where uploaded sound files are written; AudioFSDir is the path
	// prefix FreeSWITCH plays them from (default equal — one shared mount). See
	// internal/config and handlers_audio.go.
	AudioDir   string
	AudioFSDir string

	// Cache is the shared Redis cache. The API uses it only to invalidate
	// flow definitions the orchestrator caches (handlers_flows.go). May be
	// nil — invalidation becomes a no-op (the orchestrator then relies on
	// the TTL, or is itself uncached).
	Cache *cache.Cache

	// Logger is the structured logger; nil falls back to slog.Default().
	Logger *slog.Logger
}

type Server struct {
	db          *db.DB
	q           *queries.Queries // named SQL loaded from queries.sql at startup
	jwtSecret   []byte
	fsBasicUser string
	fsBasicPass string
	fs          *freeswitch.Client
	notifier    *webhook.Notifier
	cache       *cache.Cache    // shared Redis; used to invalidate cached IVR flows
	presence    *presence.Store // agent online status published by the gateway

	// agentTransport / agentGateway: how an agent's own call leg is routed
	// ("sip" → user/<sip>; "gateway" → the media gateway as a SIP endpoint).
	agentTransport string
	agentGateway   string
	// gatewayPublicURL: browser-reachable gateway base URL, returned to
	// visitors in a guest mint response.
	gatewayPublicURL string

	// audioDir / audioFSDir: where uploaded sounds are written vs the path
	// FreeSWITCH plays them from (default equal). See handlers_audio.go.
	audioDir   string
	audioFSDir string

	// Resolved iceServers built once at startup from STUN/TURN config and
	// handed to the guest mint. Empty slice == "no ICE servers configured",
	// which is valid (works on LANs).
	iceServers []iceServer

	// Round-robin trunk selection state. Lazily initialised in
	// nextRoundRobin (see trunks.go).
	rrMu sync.Mutex
	rr   map[uuid.UUID]int

	// Interactive IVR simulator sessions. In-memory, process-local,
	// TTL'd on idle — they're a developer convenience, not durable
	// state. See internal/orchestrator/sim_session.go.
	simManager *orchestrator.SimManager

	log *slog.Logger
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
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "api")
	return &Server{
		db:               cfg.DB,
		q:                queries.MustLoad(),
		jwtSecret:        []byte(cfg.JWTSecret),
		fsBasicUser:      user,
		fsBasicPass:      pass,
		fs:               freeswitch.New(cfg.ESLAddr, cfg.ESLPassword),
		notifier:         webhook.New(cfg.DB).WithLogger(cfg.Logger),
		cache:            cfg.Cache,
		presence:         presence.New(cfg.Cache),
		agentTransport:   cfg.AgentTransport,
		agentGateway:     cfg.AgentGateway,
		gatewayPublicURL: cfg.GatewayPublicURL,
		audioDir:         cfg.AudioDir,
		audioFSDir:       cfg.AudioFSDir,
		iceServers:       buildICEServers(cfg),
		simManager:       orchestrator.NewSimManager(),
		log:              logger,
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
		// Public console-auth endpoint: takes email+password, returns a
		// JWT. Lives outside the AuthRequired group because by definition
		// the caller has no credentials yet.
		r.Post("/console/login", s.handleConsoleLogin)
		// NOTE: the public guest mint (/sdk/guest-credentials) was removed —
		// all visitor tokens are minted server-side via POST /v1/guest-credentials
		// (tenant API key, below), so there's no unauthenticated mint surface.

		r.Group(func(r chi.Router) {
			r.Use(s.AuthRequired)
			r.Get("/tenants/me", s.handleGetMyTenant)
			r.Patch("/tenants/me", s.handlePatchMyTenant)
			r.Get("/guest-config", s.handleGetGuestConfig)
			r.Patch("/guest-config", s.handlePatchGuestConfig)
			// Tenant API keys (tk_live_…) — issue/list/revoke. The admin
			// manages these with the console JWT; the issued keys then auth
			// server-to-server callers via AuthRequired.
			r.Post("/api-keys", s.handleCreateAPIKey)
			r.Get("/api-keys", s.handleListAPIKeys)
			r.Delete("/api-keys/{id}", s.handleRevokeAPIKey)
			// API-key mint: integrator backend mints guest creds with a
			// trusted client_ref (the verified logged-in caller). The public
			// /sdk/guest-credentials never accepts client_ref.
			r.Post("/guest-credentials", s.handleAuthGuestCredentials)
			r.Post("/agents", s.handleCreateAgent)
			r.Get("/agents", s.handleListAgents)
			r.Get("/agents/{id}", s.handleGetAgent)
			r.Post("/agents/{id}/availability", s.handleSetAgentAvailability)
			r.Get("/agents/{id}/token", s.handleAgentToken)
			// Teams — agent groups the IVR transfer node rings as a unit.
			// Identified by name, so the path param is the team name.
			r.Post("/teams", s.handleCreateTeam)
			r.Get("/teams", s.handleListTeams)
			r.Get("/teams/{name}", s.handleGetTeam)
			r.Patch("/teams/{name}", s.handlePatchTeam)
			r.Delete("/teams/{name}", s.handleDeleteTeam)
			r.Post("/teams/{name}/agents", s.handleAddTeamAgent)
			r.Delete("/teams/{name}/agents/{agentID}", s.handleRemoveTeamAgent)
			// Live queue depth per team (orchestrator publishes → Redis → here).
			r.Get("/queues", s.handleListQueues)
			// Sound library — uploaded audio reused for IVR prompts + hold music.
			r.Get("/audio", s.handleListAudio)
			r.Post("/audio", s.handleUploadAudio)
			r.Get("/audio/{id}/file", s.handleGetAudioFile)
			r.Delete("/audio/{id}", s.handleDeleteAudio)
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
			r.Post("/flows/{id}/simulate", s.handleSimulateFlow)
			r.Post("/flows/{id}/simulate/start", s.handleSimulateStart)
			r.Post("/flows/simulate/sessions/{sid}/input", s.handleSimulateInput)

			r.Post("/trunks", s.handleCreateTrunk)
			r.Get("/trunks", s.handleListTrunks)
			r.Get("/trunks/{id}", s.handleGetTrunk)
			r.Patch("/trunks/{id}", s.handlePatchTrunk)
			r.Delete("/trunks/{id}", s.handleDeleteTrunk)
			r.Post("/trunks/reload", s.handleReloadTrunks)
		})

		// No agent-JWT-scoped routes: the SDK has NO Tring API access (ADR-001).
		// Outbound origination is backend-only (POST /v1/calls, API key); the
		// agent JWT authenticates the gateway WebSocket and nothing else.
	})

	// Browser widget bundles (embedded). Served at /sdk/voice.js,
	// /sdk/agent.js, /sdk/tring.js on the same origin as the API, so a
	// self-hoster's embed snippet works with no CDN. Mounted only if
	// `make sdk` populated the bundles.
	//
	// The paths are UNversioned, so we can't cache-forever (a deploy that
	// updates a bundle would be invisible to browsers holding a stale copy).
	// http.FileServer sets ETag/Last-Modified; "no-cache" tells the browser
	// to revalidate every time — it still gets a cheap 304 when unchanged,
	// but picks up a new bundle the instant we ship one.
	if wFS, ok := widget.FS(); ok {
		sdk := http.StripPrefix("/sdk/", http.FileServer(http.FS(wFS)))
		r.Get("/sdk/*", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			sdk.ServeHTTP(w, req)
		})
	}

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
			// index.html references content-hashed asset bundles, so it MUST
			// revalidate on every load — otherwise a browser keeps a cached
			// index pointing at a stale bundle after a deploy and silently
			// shows the old UI. The hashed assets themselves are immutable, so
			// the FileServer's ETag/Last-Modified handles them. (Mirrors the
			// /sdk no-cache fix.)
			w.Header().Set("Cache-Control", "no-cache")
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

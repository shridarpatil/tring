package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/brain"
	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/presence"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/tts"
	"github.com/shridarpatil/tring/internal/webhook"
	"github.com/shridarpatil/tring/internal/webui"
	"github.com/shridarpatil/tring/internal/widget"
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
	// TURNSecret, when set, switches TURN auth to ephemeral coturn REST-API
	// credentials minted per request (HMAC over an expiry; the secret never
	// reaches clients) instead of the static TURNUsername/TURNPassword. The
	// credential lifetime is the tenant's max_call_seconds (see turnTTLFor).
	TURNSecret string

	// AgentTransport routes the agent's own call leg: "sip" (agent registers
	// to FS directly) or "gateway" (leg routed to the media gateway as a SIP
	// endpoint; ADR-001 M4b). AgentGateway is the FS gateway name used for the
	// latter. Defaults supplied by internal/config.
	AgentTransport string
	AgentGateway   string
	// AgentJitter sets jitterbuffer_msec on the gateway agent leg ("min:max"
	// adaptive ms; "" disables). Smooths the agent's internet/TURN uplink.
	AgentJitter string

	// GatewayPublicURL is the browser-reachable base URL of the media gateway,
	// returned to visitors in a guest mint response (ADR-001 M5).
	GatewayPublicURL string

	// TTS synthesizes IVR node prompt text to an audio file at flow-save time
	// (so both planes play it via the normal audio_file path). nil/disabled →
	// text nodes without an uploaded audio file stay silent.
	TTS *tts.Engine
	// AudioDir is the shared-mount path where uploaded sound files + synthesized
	// TTS are written; the gateway reads + plays them on the media plane. See
	// internal/config and handlers_audio.go.
	AudioDir string
	// RecordingsDir is the shared-mount path the gateway writes call recordings
	// to and the API serves them from. See handleGetCallRecording.
	RecordingsDir string
	// RecordingsFormat is the recording file extension (no dot) — "ogg"/"mp3"/
	// "wav" — used to locate the local spool file. Empty defaults to "wav".
	RecordingsFormat string
	// Blob is the optional S3 store recordings are streamed from when uploaded.
	Blob *blobstore.Store

	// Cache is the shared Redis cache. The API uses it only to invalidate
	// flow definitions the brain caches (handlers_flows.go). May be
	// nil — invalidation becomes a no-op (the brain then relies on
	// the TTL, or is itself uncached).
	Cache *cache.Cache

	// Hub fans gateway events to connected SSE dashboards (nil = live streaming
	// off; the snapshot still serves). Events publishes api-originated events
	// (agent.status) onto the same bus.
	Hub    *events.Hub
	Events *events.Publisher

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
	agentJitter    string
	// gatewayPublicURL: browser-reachable gateway base URL, returned to
	// visitors in a guest mint response.
	gatewayPublicURL string

	// tts pre-renders IVR node prompt text to an audio file on flow save. nil →
	// text nodes stay silent (no Piper). See handlers_flows.go.
	tts *tts.Engine

	// audioDir: shared-mount path where uploaded sounds + TTS are written and
	// the gateway plays them from on the media plane. See handlers_audio.go.
	audioDir string

	// recordingsDir: where FreeSWITCH writes call recordings; the API reads them
	// back from the same shared-mount path to serve. See handleGetCallRecording.
	recordingsDir string
	// recordingsFormat: file extension (no dot) of the local spool — "ogg"/"mp3"/"wav".
	recordingsFormat string
	// blob: optional S3 store. When a call's recording lives in S3, the serve
	// endpoint streams it from here instead of the local dir. nil = local only.
	blob *blobstore.Store

	// iceServers holds the STATIC ICE entries (STUN, and static TURN as a
	// fallback when no turnSecret is set), built once at startup. When turnSecret
	// is set, an ephemeral TURN entry is appended per request — see iceServersFor.
	iceServers []iceServer
	turnURL    string // for per-request ephemeral TURN entries
	turnSecret string // empty → static TURN (legacy); set → ephemeral

	// Round-robin trunk selection state. Lazily initialised in
	// nextRoundRobin (see trunks.go).
	rrMu sync.Mutex
	rr   map[uuid.UUID]int

	// Interactive IVR simulator sessions. In-memory, process-local,
	// TTL'd on idle — they're a developer convenience, not durable
	// state. See internal/brain/sim_session.go.
	simManager *brain.SimManager

	hub    *events.Hub
	events *events.Publisher

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
		agentJitter:      cfg.AgentJitter,
		gatewayPublicURL: cfg.GatewayPublicURL,
		tts:              cfg.TTS,
		audioDir:         cfg.AudioDir,
		recordingsDir:    cfg.RecordingsDir,
		recordingsFormat: cfg.RecordingsFormat,
		blob:             cfg.Blob,
		iceServers:       buildICEServers(cfg),
		turnURL:          cfg.TURNURL,
		turnSecret:       cfg.TURNSecret,
		simManager:       brain.NewSimManager(),
		hub:              cfg.Hub,
		events:           cfg.Events,
		log:              logger,
	}
}

// buildICEServers turns the STUN/TURN env config into the STATIC iceServers
// array. Order matters: STUN first so the client tries direct candidates before
// falling back to TURN relay (which costs the operator bandwidth). Static TURN
// is included ONLY as the legacy fallback — when a TURN URL is set but no
// turn_secret; with a secret, TURN is minted per request (iceServersFor).
func buildICEServers(cfg Config) []iceServer {
	out := []iceServer{}
	if cfg.STUNURL != "" {
		out = append(out, iceServer{URLs: cfg.STUNURL})
	}
	if cfg.TURNURL != "" && cfg.TURNSecret == "" {
		out = append(out, iceServer{
			URLs:       cfg.TURNURL,
			Username:   cfg.TURNUsername,
			Credential: cfg.TURNPassword,
		})
	}
	return out
}

// iceServersFor returns the ICE list to hand a client: the static STUN (and
// legacy static TURN) entries, plus — when turn_secret is configured — a freshly
// minted EPHEMERAL TURN entry valid for ttl. userid (a client_ref or agent id)
// is folded into the TURN username for traceability. Generated per request so
// each client gets its own short-lived, non-reusable credential. ttl should be
// the tenant's max call duration plus a setup buffer (see turnTTLFor).
func (s *Server) iceServersFor(userid string, ttl time.Duration) []iceServer {
	if s.turnSecret == "" || s.turnURL == "" {
		return s.iceServers
	}
	user, cred := turnCredential(s.turnSecret, userid, ttl)
	out := make([]iceServer, 0, len(s.iceServers)+1)
	out = append(out, s.iceServers...)
	out = append(out, iceServer{URLs: s.turnURL, Username: user, Credential: cred})
	return out
}

// turnSetupBuffer pads the TURN credential lifetime beyond the tenant's max call
// duration to cover the mint→call-start gap (and clock skew), so a credential
// minted at "talk to human" comfortably outlives a full-length call.
const turnSetupBuffer = 5 * time.Minute

// defaultMaxCallSeconds is the fallback TURN-credential lifetime basis when a
// tenant's max_call_seconds is unreadable (4h) — generous enough not to cut a
// real call short.
const defaultMaxCallSeconds = 14400

// turnTTLFor returns the ephemeral-TURN credential lifetime for a tenant: its
// configured max_call_seconds + the setup buffer. Returns 0 (and skips the DB
// read) when ephemeral TURN is off, so a static/no-TURN deploy pays nothing.
func (s *Server) turnTTLFor(ctx context.Context, tenantID uuid.UUID) time.Duration {
	if s.turnSecret == "" {
		return 0
	}
	secs := defaultMaxCallSeconds
	if err := s.db.Pool.QueryRow(ctx, s.q.GetTenantMaxCallSeconds, tenantID).Scan(&secs); err != nil || secs <= 0 {
		secs = defaultMaxCallSeconds
	}
	return time.Duration(secs)*time.Second + turnSetupBuffer
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// CORS must come BEFORE the auth wall — preflight OPTIONS requests
	// from browsers don't carry `Authorization`, so they'd otherwise 401.
	r.Use(corsMiddleware)

	// gzip on the wire (Accept-Encoding permitting) for the compressible
	// responses. Applied selectively below — to the embedded static mounts
	// (/sdk, /admin) and to the /v1 JSON group — but deliberately NOT to the
	// SSE stream (/v1/stream/dashboard sits outside the compressed group so
	// its chunked frames are never buffered) nor to audio/recording bytes
	// (their content-types fall outside Compress's default list, so they pass
	// through uncompressed and stay Range-seekable).
	compress := middleware.Compress(5)

	r.Get("/healthz", s.handleHealthz)

	// FreeSWITCH-facing endpoints. Basic Auth shared with mod_xml_curl;
	// not behind the tenant API-key wall.
	r.Route("/fs", func(r chi.Router) {
		r.Use(middleware.Timeout(30 * time.Second))
		r.Use(s.FSBasicAuth)
		r.Post("/directory", s.handleFSDirectory)
	})

	r.Route("/v1", func(r chi.Router) {
		// Tenant-scoped routes: API key auth, full access within the tenant.
		// Public console-auth endpoint: takes email+password, returns a
		// JWT. Lives outside the AuthRequired group because by definition
		// the caller has no credentials yet.
		r.With(middleware.Timeout(30 * time.Second)).Post("/console/login", s.handleConsoleLogin)
		// NOTE: the public guest mint (/sdk/guest-credentials) was removed —
		// all visitor tokens are minted server-side via POST /v1/guest-credentials
		// (tenant API key, below), so there's no unauthenticated mint surface.

		r.Group(func(r chi.Router) {
			r.Use(s.AuthRequired)

			// Long-lived SSE: must NOT inherit the 30s request timeout.
			r.Get("/stream/dashboard", s.handleDashboardStream)

			r.Group(func(r chi.Router) {
				r.Use(middleware.Timeout(30 * time.Second))
				// gzip the JSON list/detail payloads (calls, events,
				// deliveries, agents, flows, …). The SSE stream is a sibling
				// of this group, so it's structurally excluded.
				r.Use(compress)
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
				// "Today so far" aggregates for the dashboard trend strip.
				r.Get("/stats/overview", s.handleStatsOverview)
				// Sound library — uploaded audio reused for IVR prompts + hold music.
				r.Get("/audio", s.handleListAudio)
				r.Post("/audio", s.handleUploadAudio)
				r.Get("/audio/{id}/file", s.handleGetAudioFile)
				r.Get("/audio/preview", s.handlePreviewAudio) // play by file_path (assets + TTS prompts)
				r.Delete("/audio/{id}", s.handleDeleteAudio)
				r.Post("/numbers", s.handleCreateNumber)
				r.Get("/numbers", s.handleListNumbers)
				r.Patch("/numbers/{id}", s.handlePatchNumber)

				r.Post("/calls", s.handleCreateCall)
				r.Post("/calls/connect", s.handleConnectCall)
				r.Get("/calls", s.handleListCalls)
				r.Get("/calls/{id}", s.handleGetCall)
				r.Get("/calls/{id}/events", s.handleGetCallEvents)
				r.Get("/calls/{id}/recording", s.handleGetCallRecording)
				// Backend-mediated call control (CRM backend, API key). ADR-001.
				r.Post("/calls/{id}/hold", s.handleHoldCall)
				r.Post("/calls/{id}/resume", s.handleResumeCall)

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
		})

		// Console-only: supervise a live web call. Separate from AuthRequired
		// because the supervise permission is per-user and an API key has no
		// user identity to check it against.
		r.Group(func(r chi.Router) {
			r.Use(s.ConsoleAuthRequired)
			r.Post("/calls/{id}/supervise", s.handleSupervise)
		})

		// Agent-JWT scoped SDK routes. ADR-001 keeps origination backend-only,
		// but SELF call-control (hold/transfer of the agent's OWN active call) is
		// a safe agent capability — the JWT scopes it to that agent, so the widget
		// drives it directly with the token it already holds (no CRM proxy).
		r.Group(func(r chi.Router) {
			r.Use(s.AgentAuthRequired)
			r.Use(middleware.Timeout(30 * time.Second))
			r.Post("/sdk/calls/hold", s.handleSdkHold)
			r.Post("/sdk/calls/resume", s.handleSdkResume)
			r.Get("/sdk/transfer-targets", s.handleSdkTransferTargets)
			r.Post("/sdk/calls/transfer", s.handleSdkTransfer)
		})
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
	//
	// The embedded JS/CSS/HTML bundles ship uncompressed on disk, so gzip
	// them on the wire (Accept-Encoding permitting) via the shared `compress`
	// middleware defined above. Fonts (woff2) are already compressed and fall
	// outside Compress's default type list, so they pass through untouched.
	if wFS, ok := widget.FS(); ok {
		sdk := http.StripPrefix("/sdk/", http.FileServer(http.FS(wFS)))
		r.With(compress).Get("/sdk/*", func(w http.ResponseWriter, req *http.Request) {
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
		r.With(compress).Get("/admin/*", func(w http.ResponseWriter, req *http.Request) {
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

// pageEnvelope is the uniform shape every paginated list endpoint returns:
// the rows plus the total matching the query (before LIMIT) and the window
// that produced them. Items is always a non-nil slice so it marshals to [].
type pageEnvelope struct {
	Items  interface{} `json:"items"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

// pageParams reads the optional 1-based `page` + `limit` query params. `paged`
// is false when no `page` is present, so the caller can fall back to its own
// default volume (the no-page envelope still carries a total). `limit` defaults
// to defaultPageSize and is clamped to maxPageSize.
func pageParams(r *http.Request) (limit, offset int, paged bool) {
	q := r.URL.Query()
	paged = q.Get("page") != ""
	limit = defaultPageSize
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		offset = (n - 1) * limit
	}
	return
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

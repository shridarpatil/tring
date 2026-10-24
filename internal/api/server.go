package api

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
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
	"github.com/shridarpatil/tring/internal/secretbox"
	"github.com/shridarpatil/tring/internal/tts"
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

	// TrustedProxies are the CIDR prefixes of the reverse proxies sitting in
	// front of the API (e.g. "10.0.0.0/8", "127.0.0.1/32"). Only hops inside
	// these ranges are trusted to have set X-Forwarded-For; see realIP. Empty
	// (the default) means "no proxy" — the transport address is the client
	// and XFF is ignored entirely.
	TrustedProxies []string

	// OutboundAllow are private CIDRs tenant-supplied URLs (webhooks) may
	// still reach — the operator's escape hatch for an on-prem CRM. Empty (the
	// default) = public internet only. See internal/safehttp.
	OutboundAllow []netip.Prefix

	// Secrets seals trunk and agent SIP passwords at rest (TRING_SECRETS_KEY).
	// nil = no key configured: new passwords are stored as plaintext and only
	// legacy plaintext can be read. See secrets.go.
	Secrets *secretbox.Box

	// Version is the build's version (-X main.version), shown in the console.
	Version string
}

type Server struct {
	db          *db.DB
	q           *queries.Queries // named SQL loaded from queries.sql at startup
	jwtSecret   []byte
	fsBasicUser string
	fsBasicPass string
	fs          *freeswitch.Client
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

	// trustedProxies are the CIDRs whose X-Forwarded-For entries we believe.
	// Empty = trust none. See realIP.
	trustedProxies []string

	// loginByIP / loginByEmail throttle POST /v1/console/login (see
	// attemptLimiter). In-process: per replica.
	loginByIP    *attemptLimiter
	loginByEmail *attemptLimiter

	// outboundAllow: private CIDRs webhook URLs may target (validated on create;
	// enforced again at delivery by the notifier's safehttp client).
	outboundAllow []netip.Prefix

	secrets *secretbox.Box // nil = plaintext mode (no TRING_SECRETS_KEY)

	version string

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
		cache:            cfg.Cache,
		presence:         presence.New(cfg.Cache),
		agentTransport:   cfg.AgentTransport,
		agentGateway:     cfg.AgentGateway,
		agentJitter:      cfg.AgentJitter,
		gatewayPublicURL: cfg.GatewayPublicURL,
		tts:              cfg.TTS,
		audioDir:         cfg.AudioDir,
		recordingsDir:    cfg.RecordingsDir,
		blob:             cfg.Blob,
		iceServers:       buildICEServers(cfg),
		turnURL:          cfg.TURNURL,
		turnSecret:       cfg.TURNSecret,
		simManager:       brain.NewSimManager(),
		hub:              cfg.Hub,
		events:           cfg.Events,
		trustedProxies:   cfg.TrustedProxies,
		loginByIP:        newAttemptLimiter(loginAttemptLimit, loginAttemptWindow, loginLimiterKeys),
		loginByEmail:     newAttemptLimiter(loginAttemptLimit, loginAttemptWindow, loginLimiterKeys),
		outboundAllow:    cfg.OutboundAllow,
		secrets:          cfg.Secrets,
		version:          cfg.Version,
		log:              logger,
	}
}

// realIP returns the middleware that makes r.RemoteAddr the real client IP.
//
// It replaces chi's middleware.RealIP, which is deprecated and spoofable
// (GHSA-3fxj-6jh8-hvhx, GHSA-rjr7-jggh-pgcp, GHSA-9g5q-2w5x-hmxf): RealIP
// believes True-Client-IP / X-Real-IP / X-Forwarded-For from ANY caller, so
// anyone who can reach the API directly forges the IP in our access log.
//
// With trustedProxies set we walk X-Forwarded-For right-to-left and skip only
// hops inside those CIDRs; the first untrusted entry is the client. With none
// set we leave RemoteAddr alone — an unproxied deployment must not let callers
// invent their own address. Panics at startup on an invalid CIDR, which is the
// right time to find out.
func realIP(trustedProxies []string) func(http.Handler) http.Handler {
	if len(trustedProxies) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	fromXFF := middleware.ClientIPFromXFF(trustedProxies...)
	return func(next http.Handler) http.Handler {
		return fromXFF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// chi's ClientIPFrom* deliberately never mutate RemoteAddr, but
			// middleware.Logger (and anything else reading the transport
			// address) still wants the resolved client — copy it across.
			if ip := middleware.GetClientIP(r.Context()); ip != "" {
				r.RemoteAddr = ip
			}
			next.ServeHTTP(w, r)
		}))
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

// FSRouter serves the FreeSWITCH-facing /fs/* endpoints. Basic Auth shared
// with mod_xml_curl; not behind the tenant API-key wall. Mounted on its own
// loopback listener (fs_addr), never on the public Router.
func (s *Server) FSRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Route("/fs", func(r chi.Router) {
		r.Use(middleware.Timeout(30 * time.Second))
		r.Use(s.FSBasicAuth)
		r.Post("/directory", s.handleFSDirectory)
	})
	return r
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(realIP(s.trustedProxies))
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	// CORS must come BEFORE the auth wall — preflight OPTIONS requests
	// from browsers don't carry `Authorization`, so they'd otherwise 401.
	r.Use(corsMiddleware)
	// After CORS so a browser can read the 413.
	r.Use(limitBody)

	r.Get("/healthz", s.handleHealthz)

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

				// ── Any caller, including a 'calls'-scoped API key: what a CRM
				// backend needs to place and handle calls, plus read-only lookups.
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
				r.Get("/teams", s.handleListTeams)
				r.Get("/teams/{name}", s.handleGetTeam)
				// Live queue depth per team (orchestrator publishes → Redis → here).
				r.Get("/queues", s.handleListQueues)
				// "Today so far" aggregates for the dashboard trend strip.
				r.Get("/stats/overview", s.handleStatsOverview)
				r.Get("/numbers", s.handleListNumbers)
				r.Get("/flows", s.handleListFlows)
				r.Get("/flows/{id}", s.handleGetFlow)

				r.Post("/calls", s.handleCreateCall)
				r.Post("/calls/connect", s.handleConnectCall)
				r.Get("/calls", s.handleListCalls)
				r.Get("/calls/{id}", s.handleGetCall)
				r.Get("/calls/{id}/events", s.handleGetCallEvents)
				r.Get("/calls/{id}/recording", s.handleGetCallRecording)
				// Backend-mediated call control (CRM backend, API key). ADR-001.
				r.Post("/calls/{id}/hold", s.handleHoldCall)
				r.Post("/calls/{id}/resume", s.handleResumeCall)

				// ── Full access only (console sessions, 'full' API keys): tenant
				// administration. A 'calls' key gets 403, so a leaked CRM key can't
				// mint more keys, read trunk config or repoint webhooks.
				r.Group(func(r chi.Router) {
					r.Use(s.RequireFullScope)
					r.Get("/tenants/me", s.handleGetMyTenant)
					r.Get("/version", s.handleVersion)
					r.Patch("/tenants/me", s.handlePatchMyTenant)
					r.Get("/guest-config", s.handleGetGuestConfig)
					r.Patch("/guest-config", s.handlePatchGuestConfig)
					// Tenant API keys (tk_live_…) — issue/list/revoke. The admin
					// manages these with the console JWT; the issued keys then auth
					// server-to-server callers via AuthRequired.
					r.Post("/api-keys", s.handleCreateAPIKey)
					r.Get("/api-keys", s.handleListAPIKeys)
					r.Delete("/api-keys/{id}", s.handleRevokeAPIKey)
					r.Post("/teams", s.handleCreateTeam)
					r.Patch("/teams/{name}", s.handlePatchTeam)
					r.Delete("/teams/{name}", s.handleDeleteTeam)
					r.Post("/teams/{name}/agents", s.handleAddTeamAgent)
					r.Delete("/teams/{name}/agents/{agentID}", s.handleRemoveTeamAgent)
					// Sound library — uploaded audio reused for IVR prompts + hold music.
					r.Get("/audio", s.handleListAudio)
					r.Post("/audio", s.handleUploadAudio)
					r.Get("/audio/{id}/file", s.handleGetAudioFile)
					r.Get("/audio/preview", s.handlePreviewAudio) // play by file_path (assets + TTS prompts)
					r.Delete("/audio/{id}", s.handleDeleteAudio)
					r.Post("/numbers", s.handleCreateNumber)
					r.Patch("/numbers/{id}", s.handlePatchNumber)
					r.Delete("/numbers/{id}", s.handleDeleteNumber)

					r.Post("/webhooks", s.handleCreateWebhook)
					r.Get("/webhooks", s.handleListWebhooks)
					r.Patch("/webhooks/{id}", s.handleUpdateWebhook)
					r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
					r.Get("/webhooks/{id}/deliveries", s.handleListDeliveries)

					r.Post("/flows", s.handleCreateFlow)
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
		// agent JWT authenticates the gateway WebSocket and the routes above only.
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
		sdk := http.StripPrefix("/sdk/", sdkHandler(wFS))
		r.Get("/sdk/*", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			sdk.ServeHTTP(w, req)
		})
	}

	// Admin console (embedded Vue dist). Mounted only if the Vue app was
	// actually built — backend-only deployments get a UI-less binary
	// automatically (no flag needed, just don't run `npm run build`).
	if uiFS, ok := webui.FS(); ok {
		admin := r.With(adminHeaders(adminCSP(s.gatewayPublicURL)))
		fileServer := http.FileServer(http.FS(uiFS))
		// Strip "/admin" prefix so requests for /admin/assets/foo.js
		// resolve to "assets/foo.js" within the embedded FS.
		stripped := http.StripPrefix("/admin", fileServer)
		// Catch-all: any /admin/* path that doesn't match a static file
		// falls back to index.html so the Vue router (history mode) can
		// own the URL space client-side.
		admin.Get("/admin", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/admin/", http.StatusMovedPermanently)
		})
		admin.Get("/admin/*", func(w http.ResponseWriter, req *http.Request) {
			// If it's a known asset (has a file extension), serve it
			// directly; otherwise serve index.html for SPA routing.
			path := strings.TrimPrefix(req.URL.Path, "/admin/")
			if path != "" {
				// Directories fall through to index.html — never a listing.
				if st, err := fs.Stat(uiFS, path); err == nil && !st.IsDir() {
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

// handleVersion reports the build version for the console footer. Behind auth
// so the exact build isn't advertised to anyone probing the API.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
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

// writeServerError answers a server-side failure (5xx) with a generic msg and
// logs the underlying err with the request id. Internal detail — Postgres
// errors, FreeSWITCH replies, file paths — must never reach the client: it
// leaks schema/infra and is useless to an integrator anyway.
func (s *Server) writeServerError(w http.ResponseWriter, r *http.Request, status int, msg string, err error) {
	s.log.Error(msg, "err", err, "status", status,
		"method", r.Method, "path", r.URL.Path,
		"req_id", middleware.GetReqID(r.Context()))
	writeError(w, status, msg)
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

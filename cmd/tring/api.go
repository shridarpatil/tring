package main

// api.go — the api role: stateless REST, admin console, token minting,
// recordings serving, outbound originate over ESL, and the /fs/* callbacks.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shridarpatil/tring/internal/api"
	"github.com/shridarpatil/tring/internal/bootstrap"
	"github.com/shridarpatil/tring/internal/campaign"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/events"
	tringmigrate "github.com/shridarpatil/tring/internal/migrate"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/tts"
)

type apiRole struct {
	d            *deps
	srv, fsSrv   *http.Server
	hub          *events.Hub
	cancel       context.CancelFunc // stops the hub
	db           *db.DB
	campaignDone chan struct{}
	gwSyncDone   chan struct{}
}

// openDB migrates, connects and runs first-boot bootstrap: what both the api
// role and --seed-tenant need before touching tenants.
func openDB(ctx context.Context, d *deps, dsn string) (*db.DB, error) {
	// Apply embedded schema migrations before opening the pool so the
	// schema is at the expected version when handlers start running.
	// Idempotent — no-op when the DB is already at the latest version.
	if err := tringmigrate.Run(dsn); err != nil {
		return nil, fmt.Errorf("migrate failed: %w", err)
	}
	database, err := db.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("db connect failed: %w", err)
	}
	// Auto-seed the initial tenant + admin user from env on first boot
	// against an empty database. No-op on every subsequent boot, and a
	// no-op if the operator hasn't set the bootstrap env vars at all.
	if err := bootstrap.Run(ctx, database, bootstrap.Config{
		TenantName:    d.k.String("bootstrap_tenant"),
		AdminEmail:    d.k.String("admin_email"),
		AdminPassword: d.k.String("admin_password"),
	}); err != nil {
		database.Close()
		return nil, fmt.Errorf("bootstrap failed: %w", err)
	}
	return database, nil
}

// runSeed creates one tenant and prints its API key. It never starts a listener.
func runSeed(d *deps, o options) error {
	ctx := context.Background()
	database, err := openDB(ctx, d, o.dsn)
	if err != nil {
		return err
	}
	defer database.Close()
	return seedTenant(ctx, database, o.seedTenant)
}

func startAPI(d *deps, o options) (*apiRole, error) {
	k, logger := d.k, d.log
	// The hub outlives the signal: it runs until shutdown, so the console keeps
	// live updates while the gateway role drains.
	ctx, cancel := context.WithCancel(context.Background())
	a := &apiRole{d: d, cancel: cancel}

	database, err := openDB(ctx, d, o.dsn)
	if err != nil {
		cancel()
		return nil, err
	}
	a.db = database

	// Seal legacy plaintext SIP passwords (key set), or refuse to run keyless
	// against sealed ones. Before serving: the gateway XML render needs it.
	if err := api.PrepareStoredSecrets(ctx, database, d.secrets, logger); err != nil {
		a.close()
		return nil, fmt.Errorf("stored SIP passwords: %w", err)
	}

	// Local Piper TTS: synthesizes IVR node prompt text to an audio file on flow
	// save, into <audio_dir>/tts (alongside uploaded sounds, so both planes read
	// it). Auto-enables only when piper + model resolve; otherwise a no-op.
	ttsEngine := tts.New(tts.Config{
		PiperBin: k.String("tts_piper_bin"),
		Model:    k.String("tts_piper_model"),
		Voice:    k.String("tts_voice"),
		CacheDir: filepath.Join(k.String("audio_dir"), "tts"),
		Logger:   logger,
	})

	// Dashboard live-update bus (shares the same Redis as the gateway).
	// Best-effort: if Redis is down, the dashboard still serves the connect-time
	// snapshot, just no live deltas.
	a.hub, err = events.NewHub(k.String("redis_addr"), k.String("redis_password"), logger)
	if err != nil {
		logger.Warn("redis unavailable, dashboard live stream disabled", "err", err)
	}

	apiSrv := api.NewServer(api.Config{
		DB:                database,
		CampaignLimits:    campaign.Limits{Concurrent: k.Int("campaign_max_concurrent"), PerTenant: k.Int("campaign_tenant_concurrent"), StartsPerSecond: k.Int("campaign_starts_per_second"), TotalCalls: k.Int("campaign_total_call_capacity"), ReservedCalls: k.Int("campaign_reserved_calls")},
		TTS:               ttsEngine,
		JWTSecret:         k.String("jwt_secret"),
		FSBasicAuth:       k.String("fs_basic_auth"),
		ESLAddr:           k.String("esl_addr"),
		ESLPassword:       k.String("esl_password"),
		STUNURL:           k.String("stun_url"),
		TURNURL:           k.String("turn_url"),
		TURNUsername:      k.String("turn_username"),
		TURNPassword:      k.String("turn_password"),
		TURNSecret:        k.String("turn_secret"),
		AgentTransport:    k.String("agent_transport"),
		AgentGateway:      k.String("agent_gateway"),
		AgentJitter:       k.String("agent_jitter_msec"),
		GatewayPublicURL:  k.String("gateway_public_url"),
		GatewayControlURL: k.String("gateway_control_url"),
		ConsolePublicURL:  k.String("console_public_url"),
		AudioDir:          k.String("audio_dir"),
		RecordingsDir:     k.String("recordings_dir"),
		Blob:              d.blob,
		Cache:             d.cache,
		Hub:               a.hub,
		Events:            d.pub,
		TrustedProxies:    csv(k.String("trusted_proxies")),
		OutboundAllow:     d.outboundAllow,
		Secrets:           d.secrets,
		Version:           version,
		Logger:            logger,
	})

	a.campaignDone = make(chan struct{})
	go func() { defer close(a.campaignDone); apiSrv.StartCampaignWorker(ctx) }()
	a.gwSyncDone = make(chan struct{})
	go func() { defer close(a.gwSyncDone); apiSrv.StartGatewaySync(ctx) }()

	// Register live forwards + coalesced recomputers, then start the subscriber.
	// Forward/Recompute write hub maps lock-free and MUST complete before Run
	// starts reading them.
	a.hub.Forward(
		events.TypeCallRinging, events.TypeCallRouting, events.TypeCallAnswered, events.TypeCallEnded,
		events.TypeAgentPresence, events.TypeAgentStatus, events.TypeCallSupervised, events.TypeCallTransferred,
		events.TypeQueueStint,
	)
	// Stats carries SLA breaches (today + the hourly red segment), which move
	// on queue.stint as well as on the call lifecycle.
	a.hub.Recompute(
		[]string{events.TypeCallRinging, events.TypeCallAnswered, events.TypeCallEnded, events.TypeQueueStint},
		events.TypeStats, 1500*time.Millisecond, apiSrv.StatsData,
	)
	// Queues carries on_call (answered calls with an agent), which changes on the
	// call lifecycle — so call.* must trigger this recompute too, not just
	// queue.depth. Without the call.* triggers, on_call goes stale after a call
	// ends (nothing recomputes it until the next queueing activity). It also
	// carries each team's live staffing (agent.*) and waits/breaches (queue.stint).
	a.hub.Recompute(
		[]string{
			events.TypeCallRinging, events.TypeCallAnswered, events.TypeCallEnded, events.TypeQueueDepth,
			events.TypeAgentPresence, events.TypeAgentStatus, events.TypeQueueStint,
		},
		events.TypeQueues, 800*time.Millisecond, apiSrv.QueuesData,
	)
	// Team open/closed and below-minimum change with the clock, not an event
	// (Support opens at 9:00 and nobody calls): recheck once a minute and push
	// only when the frame changed.
	a.hub.Every(60*time.Second, events.TypeQueues, apiSrv.QueuesData)
	// A waiting caller crosses the SLA with no event; recheck stats the same way
	// so "SLA breaches today" keeps up with the team cards.
	a.hub.Every(60*time.Second, events.TypeStats, apiSrv.StatsData)
	go a.hub.Run(ctx)

	// ReadTimeout bounds a slow-trickled request body (slowloris); IdleTimeout
	// reaps idle keep-alives. No WriteTimeout: /v1/stream/dashboard is a
	// long-lived SSE response. The 10 MiB audio upload already runs under a 30s
	// middleware.Timeout, so ReadTimeout doesn't tighten it further.
	a.srv = &http.Server{
		Addr:              o.addr,
		Handler:           apiSrv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// FreeSWITCH mod_xml_curl endpoints on their own loopback listener, so the
	// public api_addr (and any reverse proxy in front of it) never serves them.
	a.fsSrv = &http.Server{
		Addr:              k.String("fs_addr"),
		Handler:           apiSrv.FSRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("api listening", "addr", a.srv.Addr)
		if err := a.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api listen failed", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		logger.Info("fs endpoints listening", "addr", a.fsSrv.Addr)
		if err := a.fsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("fs listen failed", "err", err)
			os.Exit(1)
		}
	}()
	return a, nil
}

func (a *apiRole) shutdown(ctx context.Context) {
	a.d.log.Info("api shutting down")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = a.srv.Shutdown(ctx)
	_ = a.fsSrv.Shutdown(ctx)
	a.close()
}

func (a *apiRole) close() {
	a.cancel()
	if a.campaignDone != nil {
		<-a.campaignDone
	}
	if a.gwSyncDone != nil {
		<-a.gwSyncDone
	}
	a.hub.Close()
	a.db.Close()
}

func seedTenant(ctx context.Context, database *db.DB, name string) error {
	plaintext, hash, prefix, err := api.GenerateKey("live")
	if err != nil {
		return err
	}
	sipDomain := "t_" + strings.ToLower(strings.ReplaceAll(name, " ", "_")) + ".tring.local"

	var id string
	err = database.Pool.QueryRow(ctx, queries.MustLoad().InsertTenant,
		name, hash, prefix, sipDomain).Scan(&id)
	if err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}

	fmt.Printf(`
Tenant created.

  ID:         %s
  Name:       %s
  SIP domain: %s

API key (save this — it will not be shown again):

  %s

Use it as: Authorization: Bearer %s

`, id, name, sipDomain, plaintext, plaintext)
	return nil
}

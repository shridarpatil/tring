package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shridarpatil/tring/internal/api"
	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/bootstrap"
	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/config"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/logging"
	tringmigrate "github.com/shridarpatil/tring/internal/migrate"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/tts"
)

// migrateRun is a tiny shim so the call site reads `migrateRun(...)`
// rather than the long aliased package name everywhere.
var migrateRun = tringmigrate.Run

func main() {
	// Config: defaults + TRING_* env (koanf). Flags below take precedence
	// by defaulting to the resolved value, so flag > env > default.
	k := config.Load()
	logger := logging.New(k.String("log_format"), k.String("log_level"))
	slog.SetDefault(logger) // so startup helpers (bootstrap) log through it too
	addr := flag.String("addr", k.String("api_addr"), "HTTP listen address")
	dsn := flag.String("dsn", k.String("dsn"), "Postgres connection string")
	seed := flag.String("seed-tenant", "", "If set, creates a tenant with the given name, prints the API key, then exits")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Apply embedded schema migrations before opening the pool so the
	// schema is at the expected version when handlers start running.
	// Idempotent — no-op when the DB is already at the latest version.
	if err := migrateRun(*dsn); err != nil {
		logger.Error("migrate failed", "err", err)
		os.Exit(1)
	}

	database, err := db.Connect(ctx, *dsn)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	// Auto-seed the initial tenant + admin user from env on first boot
	// against an empty database. No-op on every subsequent boot, and a
	// no-op if the operator hasn't set the bootstrap env vars at all.
	if err := bootstrap.Run(ctx, database, bootstrap.Config{
		TenantName:    k.String("bootstrap_tenant"),
		AdminEmail:    k.String("admin_email"),
		AdminPassword: k.String("admin_password"),
	}); err != nil {
		logger.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}

	if *seed != "" {
		if err := seedTenant(ctx, database, *seed); err != nil {
			logger.Error("seed failed", "err", err)
			os.Exit(1)
		}
		return
	}

	// Shared Redis cache. The API only uses it to invalidate IVR flow
	// definitions the orchestrator caches — must point at the SAME Redis as
	// the orchestrator. Optional: log-and-disable if unreachable.
	flowCache, err := cache.New(k.String("redis_addr"))
	if err != nil {
		logger.Warn("redis unavailable, flow-cache invalidation disabled", "err", err)
	}
	defer flowCache.Close()

	// Optional S3 store for serving call recordings (mirrors the orchestrator's
	// uploader). Misconfig → warn and serve from the local volume instead.
	blob, err := blobstore.New(blobstore.Config{
		Bucket:    k.String("s3_bucket"),
		Region:    k.String("s3_region"),
		Endpoint:  k.String("s3_endpoint"),
		AccessKey: k.String("s3_access_key"),
		SecretKey: k.String("s3_secret_key"),
		Prefix:    k.String("s3_prefix"),
		UseSSL:    k.Bool("s3_use_ssl"),
	})
	if err != nil {
		logger.Warn("S3 recording storage disabled (config error)", "err", err)
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

	srv := &http.Server{
		Addr: *addr,
		Handler: api.NewServer(api.Config{
			DB:               database,
			TTS:              ttsEngine,
			JWTSecret:        k.String("jwt_secret"),
			FSBasicAuth:      k.String("fs_basic_auth"),
			ESLAddr:          k.String("esl_addr"),
			ESLPassword:      k.String("esl_password"),
			STUNURL:          k.String("stun_url"),
			TURNURL:          k.String("turn_url"),
			TURNUsername:     k.String("turn_username"),
			TURNPassword:     k.String("turn_password"),
			TURNSecret:       k.String("turn_secret"),
			AgentTransport:   k.String("agent_transport"),
			AgentGateway:     k.String("agent_gateway"),
			AgentJitter:      k.String("agent_jitter_msec"),
			GatewayPublicURL: k.String("gateway_public_url"),
			AudioDir:         k.String("audio_dir"),
			RecordingsDir:    k.String("recordings_dir"),
			RecordingsFormat: k.String("record_format"),
			Blob:             blob,
			Cache:            flowCache,
			Logger:           logger,
		}).Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("api listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("api listen failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
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

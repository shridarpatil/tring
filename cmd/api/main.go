package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/whatomate/tring/internal/api"
	"github.com/whatomate/tring/internal/bootstrap"
	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/config"
	"github.com/whatomate/tring/internal/db"
	tringmigrate "github.com/whatomate/tring/internal/migrate"
	"github.com/whatomate/tring/internal/queries"
)

// migrateRun is a tiny shim so the call site reads `migrateRun(...)`
// rather than the long aliased package name everywhere.
var migrateRun = tringmigrate.Run

// wsURL returns the WebSocket URL the SDK uses for sip.js's transport
// against FreeSWITCH's plain-WS binding (port 5066). If configured (via
// TRING_WS_URL, e.g. wss://voice.tring.io:7443 for production) it's used
// as-is; otherwise we auto-detect the host's outbound IP, since FreeSWITCH
// binds WS to local_ip_v4, not 127.0.0.1.
func wsURL(configured string) string {
	if configured != "" {
		return configured
	}
	// "What's my outbound IP" canonical trick: dialing a UDP socket
	// resolves the local IP that would be used for that route. No
	// packet is actually sent. Falls back to 127.0.0.1 if the host
	// has no outbound route (laptop offline, CI sandbox, etc.).
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "ws://127.0.0.1:5066"
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP.String()
	return fmt.Sprintf("ws://%s:5066", ip)
}

func main() {
	// Config: defaults + TRING_* env (koanf). Flags below take precedence
	// by defaulting to the resolved value, so flag > env > default.
	k := config.Load()
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
		log.Fatalf("migrate: %v", err)
	}

	database, err := db.Connect(ctx, *dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
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
		log.Fatalf("bootstrap: %v", err)
	}

	if *seed != "" {
		if err := seedTenant(ctx, database, *seed); err != nil {
			log.Fatalf("seed: %v", err)
		}
		return
	}

	// Shared Redis cache. The API only uses it to invalidate IVR flow
	// definitions the orchestrator caches — must point at the SAME Redis as
	// the orchestrator. Optional: log-and-disable if unreachable.
	flowCache, err := cache.New(k.String("redis_addr"))
	if err != nil {
		log.Printf("cache: redis unavailable (%v) — flow-cache invalidation disabled", err)
	}
	defer flowCache.Close()

	srv := &http.Server{
		Addr: *addr,
		Handler: api.NewServer(api.Config{
			DB:               database,
			JWTSecret:        k.String("jwt_secret"),
			FSBasicAuth:      k.String("fs_basic_auth"),
			WSURL:            wsURL(k.String("ws_url")),
			ESLAddr:          k.String("esl_addr"),
			ESLPassword:      k.String("esl_password"),
			STUNURL:          k.String("stun_url"),
			TURNURL:          k.String("turn_url"),
			TURNUsername:     k.String("turn_username"),
			TURNPassword:     k.String("turn_password"),
			AgentTransport:   k.String("agent_transport"),
			AgentGateway:     k.String("agent_gateway"),
			GatewayPublicURL: k.String("gateway_public_url"),
			Cache:            flowCache,
		}).Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("tring api listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
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

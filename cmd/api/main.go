package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/whatomate/tring/internal/api"
	"github.com/whatomate/tring/internal/db"
)

const (
	defaultDSN         = "postgres://tring:tring_dev@localhost:5434/tring"
	defaultJWT         = "tring_dev_jwt_secret_change_me_in_prod"
	defaultFSAuth      = "fs:fs_dev_secret_change_me_in_prod"
	defaultESLAddr     = "127.0.0.1:8021"
	defaultESLPassword = "ClueCon"
)

// defaultWSURL is the URL the SDK uses for sip.js's WebSocket transport
// against FreeSWITCH's plain-WS binding (port 5066). FreeSWITCH binds WS
// to the host's local_ip_v4, not 127.0.0.1, so we auto-detect the
// outbound IP rather than hardcoding "localhost". Override with
// TRING_WS_URL=wss://voice.tring.io:7443 (or similar) for production.
func defaultWSURL() string {
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
	addr := flag.String("addr", ":8085", "HTTP listen address")
	dsn := flag.String("dsn", envOr("TRING_DSN", defaultDSN), "Postgres connection string")
	seed := flag.String("seed-tenant", "", "If set, creates a tenant with the given name, prints the API key, then exits")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	database, err := db.Connect(ctx, *dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	if *seed != "" {
		if err := seedTenant(ctx, database, *seed); err != nil {
			log.Fatalf("seed: %v", err)
		}
		return
	}

	srv := &http.Server{
		Addr: *addr,
		Handler: api.NewServer(api.Config{
			DB:          database,
			JWTSecret:   envOr("TRING_JWT_SECRET", defaultJWT),
			FSBasicAuth: envOr("TRING_FS_BASIC_AUTH", defaultFSAuth),
			WSURL:       envOr("TRING_WS_URL", defaultWSURL()),
			ESLAddr:     envOr("TRING_ESL_ADDR", defaultESLAddr),
			ESLPassword: envOr("TRING_ESL_PASSWORD", defaultESLPassword),
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
	err = database.Pool.QueryRow(ctx, `
		INSERT INTO tenants (name, api_key_hash, api_key_prefix, sip_domain)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, name, hash, prefix, sipDomain).Scan(&id)
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

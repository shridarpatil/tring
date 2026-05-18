// Package main: Tring orchestrator binary.
//
// Listens on the FreeSWITCH ESL inbound socket, subscribes to channel
// events, and writes CDR updates into Postgres as calls progress through
// ringing -> answered -> ended.
//
// Does NOT expose an HTTP surface — the public REST API is cmd/api. Does
// NOT issue originate commands — those go through the API's per-call ESL
// connection (see internal/freeswitch).
//
// Run with environment overrides:
//
//	TRING_DSN, TRING_ESL_ADDR, TRING_ESL_PASSWORD
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/orchestrator"
)

const (
	defaultDSN         = "postgres://tring:tring_dev@localhost:5434/tring"
	defaultESLAddr     = "127.0.0.1:8021"
	defaultESLPassword = "ClueCon"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dbase, err := db.Connect(ctx, envOr("TRING_DSN", defaultDSN))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer dbase.Close()

	r := orchestrator.New(
		dbase,
		envOr("TRING_ESL_ADDR", defaultESLAddr),
		envOr("TRING_ESL_PASSWORD", defaultESLPassword),
	)
	log.Println("tring orchestrator starting")
	if err := r.Run(ctx); err != nil {
		log.Fatalf("run: %v", err)
	}
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

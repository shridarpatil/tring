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

	"github.com/percipia/eslgo"

	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/orchestrator"
)

const (
	defaultDSN         = "postgres://tring:tring_dev@localhost:5434/tring"
	defaultESLAddr     = "127.0.0.1:8021"
	defaultESLPassword = "ClueCon"
	// IVR socket — the FreeSWITCH dialplan does
	//   <action application="socket" data="127.0.0.1:8086 async full"/>
	// for DIDs the operator wants to put through the flow builder. The
	// executor's HandleInboundCall is registered as the handler.
	defaultIVRAddr = ":8086"
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

	// IVR executor — starts an outbound-ESL listener so FreeSWITCH can
	// hand inbound calls to us via the dialplan's `socket` application.
	// Lives in this same process so the orchestrator can reuse its DB
	// pool. Listener runs in its own goroutine; it doesn't block the
	// event-loop runner below.
	ivrAddr := envOr("TRING_IVR_ADDR", defaultIVRAddr)
	exec := orchestrator.NewExecutor(dbase)
	go func() {
		log.Printf("ivr executor listening on %s", ivrAddr)
		if err := eslgo.ListenAndServe(ivrAddr, exec.HandleInboundCall); err != nil {
			log.Printf("ivr listener exited: %v", err)
		}
	}()

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

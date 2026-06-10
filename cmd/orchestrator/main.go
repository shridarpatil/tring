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
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/percipia/eslgo"

	"github.com/whatomate/tring/internal/blobstore"
	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/config"
	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/logging"
	"github.com/whatomate/tring/internal/orchestrator"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Config: defaults + TRING_* env (koanf).
	k := config.Load()
	logger := logging.New(k.String("log_format"), k.String("log_level"))
	slog.SetDefault(logger)

	dbase, err := db.Connect(ctx, k.String("dsn"))
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer dbase.Close()

	// Optional S3 store for call recordings. Misconfig → warn and run local-only
	// (recordings stay on the volume); S3 is opt-in, not required to boot.
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

	r := orchestrator.New(dbase, k.String("esl_addr"), k.String("esl_password")).
		WithRecordings(k.String("recordings_dir"), k.Int("record_sample_rate"), k.String("record_format")).
		WithBlobStore(blob).
		WithLogger(logger)

	// IVR executor — starts an outbound-ESL listener so FreeSWITCH can
	// hand inbound calls to us via the dialplan's `socket` application
	//   <action application="socket" data="127.0.0.1:8086 async full"/>
	// for DIDs the operator wants to put through the flow builder. Lives
	// in this same process so the orchestrator can reuse its DB pool;
	// runs in its own goroutine and doesn't block the event-loop runner.
	// Optional Redis cache for IVR flow definitions (cache-aside, 1h TTL,
	// invalidated by the API on flow edits). Redis is optional: if it's not
	// configured or unreachable we log and run with caching disabled rather
	// than refuse to start — correctness doesn't depend on it.
	flowCache, err := cache.New(k.String("redis_addr"))
	if err != nil {
		logger.Warn("redis unavailable, IVR flow caching disabled", "err", err)
	}
	defer flowCache.Close()

	ivrAddr := k.String("ivr_addr")
	exec := orchestrator.NewExecutor(dbase).WithCache(flowCache).
		WithAgentTransport(k.String("agent_transport"), k.String("agent_gateway")).
		WithESL(k.String("esl_addr"), k.String("esl_password")).
		WithLogger(logger)
	go func() {
		logger.Info("ivr executor listening", "addr", ivrAddr)
		if err := eslgo.ListenAndServe(ivrAddr, exec.HandleInboundCall); err != nil {
			logger.Error("ivr listener exited", "err", err)
		}
	}()

	logger.Info("orchestrator starting")
	if err := r.Run(ctx); err != nil {
		logger.Error("orchestrator run failed", "err", err)
		os.Exit(1)
	}
}

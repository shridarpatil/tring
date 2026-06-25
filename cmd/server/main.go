// Package main: Tring call-plane server (pion media + brain, served on /rtc).
//
// The web-calling plane. Browsers join as rooms on /rtc (WebSocket signaling);
// the SFU fans media between participants. The server runs the web plug with the
// brain embedded in-process — IVR and team routing happen here, not over a remote
// control channel. FreeSWITCH / PSTN adapters are not wired here.
//
// Env: TRING_GATEWAY_ADDR, TRING_STUN_URL, TRING_TURN_URL, and either
//
//	TRING_TURN_USERNAME/_PASSWORD (static creds) or TRING_TURN_SECRET (the
//	server mints its own ephemeral coturn creds, lifetime TRING_TURN_TTL_SECONDS),
//	TRING_TURN_RELAY_ONLY, TRING_PUBLIC_IP, TRING_GATEWAY_UDP_MIN/_MAX.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/whatomate/tring/internal/blobstore"
	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/callcdr"
	"github.com/whatomate/tring/internal/config"
	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/flowstore"
	"github.com/whatomate/tring/internal/logging"
	"github.com/whatomate/tring/internal/media/sfu"
	"github.com/whatomate/tring/internal/media/signaling"
	"github.com/whatomate/tring/internal/presence"
)

func main() {
	k := config.Load()

	logger := logging.New(k.String("log_format"), k.String("log_level"))
	slog.SetDefault(logger)

	turnURL := k.String("turn_url")
	turnSecret := k.String("turn_secret")

	// Redis (shared with api/orchestrator) backs agent presence. Optional:
	// log-and-disable if unreachable — presence just goes dark, calls still work.
	redisCache, err := cache.New(k.String("redis_addr"))
	if err != nil {
		logger.Warn("redis unavailable, agent presence disabled", "err", err)
	}
	defer redisCache.Close()

	// Redis is a HARD dependency for single-use gateway-call-token enforcement:
	// without it consumeToken can't prove a token is fresh and the gateway fails
	// closed on every authed offer. So when auth is required, refuse to boot
	// without a reachable Redis rather than serve 503s — the operator must set
	// TRING_REDIS_ADDR (or explicitly disable auth for an open/dev gateway).
	if k.Bool("gateway_require_auth") && redisCache == nil {
		logger.Error("gateway_require_auth is set but Redis is unavailable — " +
			"set TRING_REDIS_ADDR to a reachable Redis (single-use token enforcement " +
			"needs it), or set TRING_GATEWAY_REQUIRE_AUTH=false for an open gateway")
		os.Exit(1)
	}

	// Pion SFU media plane (internal/media) — the N-party web-calling path,
	// served on /rtc. Same ICE/TURN/public-IP config as the legacy bridge;
	// shares the media UDP range.
	var sfuICE []sfu.ICEServer
	if stun := k.String("stun_url"); stun != "" {
		sfuICE = append(sfuICE, sfu.ICEServer{URLs: []string{stun}})
	}
	if turnURL != "" && turnSecret == "" {
		sfuICE = append(sfuICE, sfu.ICEServer{
			URLs:       []string{turnURL},
			Username:   k.String("turn_username"),
			Credential: k.String("turn_password"),
		})
	}
	mediaSFU, err := sfu.New(sfu.Config{
		ICEServers: sfuICE,
		UDPPortMin: uint16(k.Int("gateway_udp_min")),
		UDPPortMax: uint16(k.Int("gateway_udp_max")),
		PublicIP:   k.String("public_ip"),
		RelayOnly:  k.Bool("turn_relay_only"),
		TURNURL:    turnURL,
		TURNSecret: turnSecret,
		TURNTTL:    time.Duration(k.Int("turn_ttl_seconds")) * time.Second,
		Logger:     logger,
	})
	if err != nil {
		logger.Error("sfu init failed", "err", err)
		os.Exit(1)
	}
	// Read-only DB for loading IVR flows (web-to-web IVR runs on the media plane).
	// Best-effort: if the DB is unreachable, web calls are plain peer-to-peer
	// (no IVR) rather than failing the gateway.
	var flows signaling.FlowLoader
	var teams signaling.TeamResolver
	var calls signaling.CallLog
	if database, dberr := db.Connect(context.Background(), k.String("dsn")); dberr != nil {
		logger.Warn("gateway DB unavailable, IVR on web calls disabled", "err", dberr)
	} else {
		store := flowstore.New(database)
		flows, teams = store, store
		// CDRs + recordings for media-plane calls (web + phone): same calls table,
		// spool dir, and S3 scheme as the FreeSWITCH path. S3 stays opt-in.
		blob, berr := blobstore.New(blobstore.Config{
			Bucket:    k.String("s3_bucket"),
			Region:    k.String("s3_region"),
			Endpoint:  k.String("s3_endpoint"),
			AccessKey: k.String("s3_access_key"),
			SecretKey: k.String("s3_secret_key"),
			Prefix:    k.String("s3_prefix"),
			UseSSL:    k.Bool("s3_use_ssl"),
		})
		if berr != nil {
			logger.Warn("S3 recording storage disabled (config error)", "err", berr)
		}
		calls = callcdr.New(database, blob, k.String("recordings_dir"), logger)
	}

	sig := signaling.New(signaling.Config{
		SFU:        mediaSFU,
		JWTSecret:  []byte(k.String("jwt_secret")),
		STUNURL:    k.String("stun_url"),
		TURNURL:    turnURL,
		TURNSecret: turnSecret,
		TURNTTL:    time.Duration(k.Int("turn_ttl_seconds")) * time.Second,
		Grace:      time.Duration(k.Int("media_grace_seconds")) * time.Second,
		DevTokens:  k.Bool("rtc_dev_tokens"),
		Flows:      flows,
		Teams:      teams,
		AudioDir:   k.String("audio_dir"),
		Tokens:     redisCache,               // single-use burn for visitor /rtc tokens (anti-replay)
		Presence:   presence.New(redisCache), // publish agent online/offline for the dashboard
		Calls:      calls,                    // CDRs + recordings (one path for web + phone)
		Cache:      redisCache,               // publish web team-queue depth for the dashboard
		Logger:     logger,
	})

	addr := k.String("gateway_addr")
	sigHandler := sig.Handler()

	// One listener serves the pion media plane only: /rtc (WebSocket signaling).
	// The legacy bridge endpoints are not wired in this binary — server
	// implements only the web plug (pion-based N-party calls on /rtc).
	root := http.NewServeMux()
	root.Handle("/rtc", sigHandler)
	root.Handle("/rtc/", sigHandler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("server listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server serve failed", "err", err)
		os.Exit(1)
	}
}

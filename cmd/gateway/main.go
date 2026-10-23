// Package main: Tring call-plane gateway (pion media + brain, served on /rtc).
//
// The web-calling plane. Browsers join as rooms on /rtc (WebSocket signaling);
// the SFU fans media between participants. The gateway runs the web plug with the
// brain embedded in-process — IVR and team routing happen here, not over a remote
// control channel. FreeSWITCH / PSTN adapters are not wired here.
//
// Env: TRING_GATEWAY_ADDR, TRING_STUN_URL, TRING_TURN_URL, and either
//
//	TRING_TURN_USERNAME/_PASSWORD (static creds) or TRING_TURN_SECRET (the
//	gateway mints its own ephemeral coturn creds, lifetime TRING_TURN_TTL_SECONDS),
//	TRING_TURN_RELAY_ONLY, TRING_PUBLIC_IP, TRING_GATEWAY_UDP_MIN/_MAX.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/config"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/flowstore"
	"github.com/shridarpatil/tring/internal/fsplug"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/media/signaling"
	"github.com/shridarpatil/tring/internal/outboundtrunks"
	"github.com/shridarpatil/tring/internal/presence"
	"github.com/shridarpatil/tring/internal/safehttp"
	"github.com/shridarpatil/tring/internal/webhook"
)

func main() {
	k := config.Load()

	logger := logging.New(k.String("log_format"), k.String("log_level"))
	slog.SetDefault(logger)
	if weak := config.WeakSecrets(k, "jwt_secret"); len(weak) > 0 && !k.Bool("allow_insecure_secrets") {
		logger.Error("refusing to start with default or short secrets; set them in .env (or TRING_ALLOW_INSECURE_SECRETS=true for local dev)", "keys", weak)
		os.Exit(1)
	}

	// SSRF guard for tenant-supplied URLs (IVR http_callback, webhooks): one
	// client, public internet + the operator's outbound_allow_cidrs only.
	outboundAllow, err := safehttp.ParseCIDRs(strings.Split(k.String("outbound_allow_cidrs"), ","))
	if err != nil {
		logger.Error("invalid outbound_allow_cidrs", "err", err)
		os.Exit(1)
	}
	ivrHTTP := safehttp.NewClient(outboundAllow, 0) // per-node timeout via ctx (ivrdrive)
	webhookHTTP := safehttp.NewClient(outboundAllow, webhook.DeliveryTimeout)

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

	// Dashboard live-update publisher (shares the Redis the api Hub subscribes
	// to). Best-effort: nil when Redis is off → events simply don't fire.
	eventsPub, err := events.NewPublisher(k.String("redis_addr"), logger)
	if err != nil {
		logger.Warn("redis unavailable, dashboard events disabled", "err", err)
	}
	defer eventsPub.Close()

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
	var holdMusic signaling.HoldMusicResolver
	var transferMessage signaling.TransferMessageResolver
	var calls signaling.CallLog
	var supervisors signaling.SupervisorLog
	var didResolver signaling.DIDResolver
	var trunkRes *outboundtrunks.Resolver
	if database, dberr := db.Connect(context.Background(), k.String("dsn")); dberr != nil {
		logger.Warn("gateway DB unavailable, IVR on web calls disabled", "err", dberr)
	} else {
		store := flowstore.New(database)
		flows, teams, holdMusic, didResolver = store, store, store, store
		transferMessage = store
		trunkRes = outboundtrunks.New(database)
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
		// One *callcdr.Log satisfies both CallLog and SupervisorLog; assign it into
		// each interface-typed var from its concrete type so a nil DB leaves BOTH
		// unset (a typed-nil stored in an interface would compare != nil and break
		// the "Calls != nil"/"Supervisors != nil" best-effort checks downstream).
		// The call log emits call.answered / call.ended webhooks: the gateway is
		// the one process that sees each media-plane call answer and end, so
		// each fires exactly once however many api replicas run.
		cdrLog := callcdr.New(database, blob, k.String("recordings_dir"), logger, eventsPub).
			WithWebhooks(webhook.New(database).WithLogger(logger).WithHTTPClient(webhookHTTP))
		calls = cdrLog
		supervisors = cdrLog
	}

	sig := signaling.New(signaling.Config{
		SFU:             mediaSFU,
		JWTSecret:       []byte(k.String("jwt_secret")),
		STUNURL:         k.String("stun_url"),
		TURNURL:         turnURL,
		TURNSecret:      turnSecret,
		TURNTTL:         time.Duration(k.Int("turn_ttl_seconds")) * time.Second,
		Grace:           time.Duration(k.Int("media_grace_seconds")) * time.Second,
		DevTokens:       k.Bool("rtc_dev_tokens"),
		Flows:           flows,
		Teams:           teams,
		HoldMusic:       holdMusic,       // tenant hold music for agent-initiated hold (msgHold)
		TransferMessage: transferMessage, // tenant no-answer prompt for blind transfer
		AudioDir:        k.String("audio_dir"),
		Tokens:          redisCache,               // single-use burn for visitor /rtc tokens (anti-replay)
		Presence:        presence.New(redisCache), // publish agent online/offline for the dashboard
		Calls:           calls,                    // CDRs + recordings (one path for web + phone)
		Cache:           redisCache,               // publish web team-queue depth for the dashboard
		Events:          eventsPub,
		Supervisors:     supervisors, // supervisor session audit (nil DB → nil, no audit)
		HTTP:            ivrHTTP,     // SSRF-guarded client for IVR http_callback nodes
		Logger:          logger,
	})

	// FreeSWITCH SIP plug (internal/fsplug) — inbound PSTN calls over
	// FreeSWITCH, routed through the room.Plug seam. OFF by default; needs
	// the flowstore DID resolver, so it's also a no-op without a reachable DB.
	if k.Bool("fsplug_enabled") {
		if didResolver == nil {
			logger.Warn("fsplug enabled but DB/flowstore unavailable — inbound phone calls disabled")
		} else {
			plug, perr := fsplug.New(fsplug.Config{
				SIPListen:    k.String("fsplug_sip_listen"),
				SIPAdvertise: k.String("fsplug_sip_advertise"),
				RTPMin:       k.Int("fsplug_rtp_min"),
				RTPMax:       k.Int("fsplug_rtp_max"),
				OpusPT:       111,
				DTMFPT:       101,
				FSSIP:        k.String("fsplug_fs_sip"),
			}, logger)
			if perr != nil {
				logger.Error("fsplug init failed", "err", perr)
				os.Exit(1)
			}
			sig.AttachPstn(plug, didResolver)
			if trunkRes != nil {
				sig.AttachOriginator(plug, trunkRes)
			}
			go func() {
				if rerr := plug.Run(context.Background()); rerr != nil {
					logger.Error("fsplug SIP server stopped", "err", rerr)
				}
			}()
			logger.Info("fsplug enabled", "sip", k.String("fsplug_sip_listen"))
		}
	}

	addr := k.String("gateway_addr")
	sigHandler := sig.Handler()

	// One listener serves the pion media plane only: /rtc (WebSocket signaling).
	// The legacy bridge endpoints are not wired in this binary — the gateway
	// implements only the web plug (pion-based N-party calls on /rtc).
	root := http.NewServeMux()
	root.Handle("/rtc", sigHandler)
	root.Handle("/rtc/", sigHandler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("gateway listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("gateway serve failed", "err", err)
		os.Exit(1)
	}
}

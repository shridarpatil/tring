package main

// gateway.go — the gateway role: the call plane. Browsers join rooms on /rtc
// (WebSocket signaling) and the pion SFU fans media between participants, with
// the brain (IVR + team routing + CDR) embedded in-process. Phone legs arrive
// through the FreeSWITCH plug (internal/fsplug) when fsplug_enabled is set.
//
// Env: TRING_GATEWAY_ADDR, TRING_STUN_URL, TRING_TURN_URL, and either
//
//	TRING_TURN_USERNAME/_PASSWORD (static creds) or TRING_TURN_SECRET (the
//	gateway mints its own ephemeral coturn creds, lifetime TRING_TURN_TTL_SECONDS),
//	TRING_TURN_RELAY_ONLY, TRING_PUBLIC_IP, TRING_GATEWAY_UDP_MIN/_MAX.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/cluster"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/flowstore"
	"github.com/shridarpatil/tring/internal/fsplug"
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/media/signaling"
	"github.com/shridarpatil/tring/internal/outboundtrunks"
	"github.com/shridarpatil/tring/internal/presence"
	"github.com/shridarpatil/tring/internal/safehttp"
	"github.com/shridarpatil/tring/internal/webhook"
)

type gatewayRole struct {
	d           *deps
	sig         *signaling.Server
	srv, ctlSrv *http.Server
	hub         *events.Hub

	cl        *cluster.Cluster // nil without Redis: single node
	stopCl    context.CancelFunc
	clStopped chan struct{} // closed when cl.Run returns
}

// clusterOrSingle decides what a failed cluster.New means. All mode must run
// exactly as a single node does without Redis, so it warns and carries on with
// a nil cluster; a gateway-only node exists to share load through the cluster
// and is useless without it, so it refuses to start.
func clusterOrSingle(mode string, cl *cluster.Cluster, err error, log *slog.Logger) (*cluster.Cluster, error) {
	if err == nil {
		return cl, nil
	}
	if mode == "gateway" {
		return nil, fmt.Errorf("cluster init failed: %w", err)
	}
	log.Warn("cluster disabled, running as a single node", "err", err)
	return nil, nil
}

// startGateway starts the call plane. mode ("all" or "gateway") is what this
// node advertises in the cluster registry.
func startGateway(d *deps, mode string) (*gatewayRole, error) {
	k, logger, redisCache := d.k, d.log, d.cache
	g := &gatewayRole{d: d}

	// Redis is a HARD dependency for single-use gateway-call-token enforcement:
	// without it consumeToken can't prove a token is fresh and the gateway fails
	// closed on every authed offer. So when auth is required, refuse to boot
	// without a reachable Redis rather than serve 503s — the operator must set
	// TRING_REDIS_ADDR (or explicitly disable auth for an open/dev gateway).
	if k.Bool("gateway_require_auth") && redisCache == nil {
		return nil, errors.New("gateway_require_auth is set but Redis is unavailable — " +
			"set TRING_REDIS_ADDR to a reachable Redis (single-use token enforcement " +
			"needs it), or set TRING_GATEWAY_REQUIRE_AUTH=false for an open gateway")
	}

	ivrHTTP := safehttp.NewClient(d.outboundAllow, 0) // per-node timeout via ctx (ivrdrive)
	webhookHTTP := safehttp.NewClient(d.outboundAllow, webhook.DeliveryTimeout)

	turnURL := k.String("turn_url")
	turnSecret := k.String("turn_secret")

	// Pion SFU media plane (internal/media) — the N-party web-calling path,
	// served on /rtc.
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
		return nil, fmt.Errorf("sfu init failed: %w", err)
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
	var webhooks signaling.Webhooks
	var didResolver signaling.DIDResolver
	var availability signaling.AvailabilityResolver
	var trunkRes *outboundtrunks.Resolver
	if database, dberr := db.Connect(context.Background(), k.String("dsn")); dberr != nil {
		logger.Warn("gateway DB unavailable, IVR on web calls disabled", "err", dberr)
	} else {
		store := flowstore.New(database).WithCache(redisCache) // DID + flow reads on every inbound call
		flows, teams, holdMusic, didResolver = store, store, store, store
		transferMessage = store
		availability = store
		trunkRes = outboundtrunks.New(database)
		// One *callcdr.Log satisfies both CallLog and SupervisorLog; assign it into
		// each interface-typed var from its concrete type so a nil DB leaves BOTH
		// unset (a typed-nil stored in an interface would compare != nil and break
		// the "Calls != nil"/"Supervisors != nil" best-effort checks downstream).
		// IVR flows send their webhooks from here: the gateway runs every flow
		// and sees each media-plane call end (call.ended to a flow's "on call
		// end" webhook), so each fires once however many api replicas run.
		notifier := webhook.New(database).WithLogger(logger).WithHTTPClient(webhookHTTP).
			WithCache(redisCache).WithSecrets(d.secrets)
		webhooks = notifier
		// CDRs + recordings for media-plane calls (web + phone): same calls
		// table, spool dir, and S3 scheme. S3 stays opt-in.
		cdrLog := callcdr.New(database, d.blob, k.String("recordings_dir"), logger, d.pub).
			WithWebhooks(notifier).WithCache(redisCache)
		calls = cdrLog
		supervisors = cdrLog
	}

	// Join the cluster: agent presence, on-call and dial counts, and rings are
	// shared with the other nodes through Redis. Only with Redis; without it
	// (all mode only, see gatewayNeedsRedis) this node runs alone.
	if redisCache != nil {
		cl, clErr := cluster.New(cluster.Config{
			Addr:     k.String("redis_addr"),
			Password: k.String("redis_password"),
			Mode:     mode,
			Version:  version,
		}, logger)
		if g.cl, err = clusterOrSingle(mode, cl, clErr, logger); err != nil {
			return nil, err
		}
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
		Events:          d.pub,
		Supervisors:     supervisors,  // supervisor session audit (nil DB → nil, no audit)
		HTTP:            ivrHTTP,      // SSRF-guarded client for IVR http_callback nodes
		Webhooks:        webhooks,     // IVR node webhooks (nil without a DB)
		Availability:    availability, // agent's Away flag sent on connect (nil without a DB)
		Cluster:         g.cl,         // nil → single node: local agent maps only
		Logger:          logger,
	})
	g.sig = sig

	// Heartbeat + ring bus until shutdown. Started before the listeners so this
	// node is visible to its peers by the time it takes calls.
	clCtx, stopCl := context.WithCancel(context.Background())
	g.stopCl, g.clStopped = stopCl, make(chan struct{})
	go func() {
		defer close(g.clStopped)
		g.cl.Run(clCtx, sig.ClusterSnapshot, sig.HandleClusterMsg)
	}()

	// Backend Away/Break toggles (the api's agent.status event) reach the SDK
	// live: forward each to the agent's presence socket if they're connected
	// here. Best-effort: without Redis the agent only learns it on reconnect.
	// Its own Hub, not the api role's: this one only uses On, and each Hub's
	// registrations must finish before its Run.
	g.hub, err = events.NewHub(k.String("redis_addr"), k.String("redis_password"), logger)
	if err != nil {
		logger.Warn("redis unavailable, live agent availability disabled", "err", err)
	}
	g.hub.On(events.TypeAgentStatus, func(tenant string, data json.RawMessage) {
		var e struct {
			Sip       string `json:"sip_username"`
			Available bool   `json:"available_for_calls"`
		}
		if json.Unmarshal(data, &e) == nil {
			sig.PushAvailability(tenant, e.Sip, e.Available)
		}
	})
	go g.hub.Run(context.Background())

	// FreeSWITCH SIP plug (internal/fsplug) — inbound PSTN calls over
	// FreeSWITCH, routed through the room.Plug seam. OFF by default; needs
	// the flowstore DID resolver, so it's also a no-op without a reachable DB.
	if k.Bool("fsplug_enabled") {
		if didResolver == nil {
			logger.Warn("fsplug enabled but DB/flowstore unavailable — inbound phone calls disabled")
		} else {
			fsAllow, aerr := safehttp.ParseCIDRs(strings.Split(k.String("fsplug_allow_cidrs"), ","))
			if aerr != nil {
				g.closeBackground()
				return nil, fmt.Errorf("invalid fsplug_allow_cidrs: %w", aerr)
			}
			plug, perr := fsplug.New(fsplug.Config{
				SIPListen:    k.String("fsplug_sip_listen"),
				SIPAdvertise: k.String("fsplug_sip_advertise"),
				RTPMin:       k.Int("fsplug_rtp_min"),
				RTPMax:       k.Int("fsplug_rtp_max"),
				OpusPT:       111,
				DTMFPT:       101,
				FSSIP:        k.String("fsplug_fs_sip"),
				AllowFrom:    fsAllow,
			}, logger)
			if perr != nil {
				g.closeBackground()
				return nil, fmt.Errorf("fsplug init failed: %w", perr)
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

	sigHandler := sig.Handler()
	root := http.NewServeMux()
	root.Handle("/rtc", sigHandler)
	root.Handle("/rtc/", sigHandler)
	root.Handle("/healthz", sigHandler) // 503 while draining

	// ReadTimeout/IdleTimeout only govern the HTTP upgrade request: Hijack
	// (inside ws.UpgradeHTTP) clears the conn deadlines, and the signaling
	// layer sets its own per-read deadlines on the socket. No WriteTimeout —
	// it would be meaningless for (and is likewise cleared on) hijacked conns.
	g.srv = &http.Server{
		Addr:              k.String("gateway_addr"),
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// The API's control calls (two-number connect) on their own loopback
	// listener, like the api's fs_addr: never reachable through gateway_addr.
	g.ctlSrv = &http.Server{
		Addr:              k.String("gateway_control_addr"),
		Handler:           sig.ControlHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logger.Info("gateway control listening", "addr", g.ctlSrv.Addr)
		if err := g.ctlSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway control serve failed", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		logger.Info("gateway listening", "addr", g.srv.Addr)
		if err := g.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway serve failed", "err", err)
			os.Exit(1)
		}
	}()
	return g, nil
}

// drain takes no new calls and lets the live ones finish, up to
// gateway_drain_seconds. Keep compose's stop_grace_period above that, or Docker
// SIGKILLs mid-drain. It returns the calls still live when it gave up.
func (g *gatewayRole) drain(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(g.d.k.Int("gateway_drain_seconds"))*time.Second)
	defer cancel()
	left := g.sig.Drain(ctx)
	if left > 0 {
		g.d.log.Warn("drain timed out, ending live calls", "calls", left)
	} else {
		g.d.log.Info("drained, shutting down")
	}
	return left
}

func (g *gatewayRole) shutdown(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = g.srv.Shutdown(ctx)
	_ = g.ctlSrv.Shutdown(ctx)
	g.closeBackground()
}

// closeBackground stops the events hub and leaves the cluster, on shutdown or
// when startGateway fails after starting them. Heartbeats stop before Close
// deletes this node's keys, so a late beat can't write them back; peers then
// drop this node at once instead of after the TTL.
func (g *gatewayRole) closeBackground() {
	g.hub.Close()
	g.stopCl()
	<-g.clStopped
	if err := g.cl.Close(); err != nil {
		g.d.log.Warn("cluster close failed", "err", err)
	}
}

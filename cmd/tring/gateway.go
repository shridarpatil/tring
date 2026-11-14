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
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/campaign"
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
	cdr         *callcdr.Log
}

func startGateway(d *deps) (*gatewayRole, error) {
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
	var transferTimeouts signaling.TransferTimeoutResolver
	var calls signaling.CallLog
	var campaignStore *campaign.Store
	var supervisors signaling.SupervisorLog
	var webhooks signaling.Webhooks
	var didResolver signaling.DIDResolver
	var availability signaling.AvailabilityResolver
	var numberMask signaling.NumberMaskResolver
	var trunkRes *outboundtrunks.Resolver
	if database, dberr := db.Connect(context.Background(), k.String("dsn")); dberr != nil {
		logger.Warn("gateway DB unavailable, IVR on web calls disabled", "err", dberr)
	} else {
		campaignStore = campaign.New(database.Pool)
		campaignStore.Limits = campaign.Limits{Concurrent: k.Int("campaign_max_concurrent"), PerTenant: k.Int("campaign_tenant_concurrent"), StartsPerSecond: k.Int("campaign_starts_per_second"), TotalCalls: k.Int("campaign_total_call_capacity"), ReservedCalls: k.Int("campaign_reserved_calls")}
		store := flowstore.New(database).WithCache(redisCache) // DID + flow reads on every inbound call
		flows, teams, holdMusic, didResolver = store, store, store, store
		transferMessage = store
		transferTimeouts = store
		availability = store
		numberMask = store
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
		cdrLog := callcdr.New(database, d.blob, k.String("recordings_dir"), logger, d.pub,
			callcdr.WithRecordingWorkers(k.Int("recording_workers"))).
			WithWebhooks(notifier).WithCache(redisCache)
		g.cdr = cdrLog
		calls = cdrLog
		supervisors = cdrLog
	}

	sig := signaling.New(signaling.Config{
		SFU:              mediaSFU,
		JWTSecret:        []byte(k.String("jwt_secret")),
		STUNURL:          k.String("stun_url"),
		TURNURL:          turnURL,
		TURNSecret:       turnSecret,
		TURNTTL:          time.Duration(k.Int("turn_ttl_seconds")) * time.Second,
		Grace:            time.Duration(k.Int("media_grace_seconds")) * time.Second,
		DevTokens:        k.Bool("rtc_dev_tokens"),
		Flows:            flows,
		Teams:            teams,
		HoldMusic:        holdMusic,       // tenant hold music for agent-initiated hold (msgHold)
		TransferMessage:  transferMessage, // tenant no-answer prompt for blind transfer
		TransferTimeouts: transferTimeouts,
		AudioDir:         k.String("audio_dir"),
		Tokens:           redisCache,               // single-use burn for visitor /rtc tokens (anti-replay)
		Presence:         presence.New(redisCache), // publish agent online/offline for the dashboard
		Calls:            calls,
		Campaigns:        campaignStore, // CDRs + recordings (one path for web + phone)
		Cache:            redisCache,    // publish web team-queue depth for the dashboard
		Events:           d.pub,
		Supervisors:      supervisors,  // supervisor session audit (nil DB → nil, no audit)
		HTTP:             ivrHTTP,      // SSRF-guarded client for IVR http_callback nodes
		Webhooks:         webhooks,     // IVR node webhooks (nil without a DB)
		Availability:     availability, // agent's Away flag sent on connect (nil without a DB)
		NumberMask:       numberMask,   // masks the caller number on masked agents' rings (nil without a DB)
		Logger:           logger,
	})
	g.sig = sig

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
		// Team member lookups filter on available_for_calls (and a SIP rename
		// publishes this too), so queued callers must re-read their team.
		sig.TeamMembersChanged(tenant)
	})
	// A member added to (or removed from) a team reaches callers already
	// queued for it. Without Redis, queued callers keep the membership they
	// read on entering the queue.
	g.hub.On(events.TypeTeamMembers, func(tenant string, _ json.RawMessage) {
		sig.TeamMembersChanged(tenant)
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
				g.hub.Close()
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
				g.hub.Close()
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
	if g.cdr != nil {
		g.cdr.ResumeRecordings()
	}
	return g, nil
}

// endCallsTimeout bounds the teardown of the calls a timed-out drain ends: BYEs,
// CDR settles and webhook dispatch, before the HTTP servers stop.
const endCallsTimeout = 10 * time.Second

// drain takes no new calls and lets the live ones finish, up to
// gateway_drain_seconds, then ends whatever is still up. Keep compose's
// stop_grace_period above gateway_drain_seconds + endCallsTimeout + the
// recording flush in shutdown, or Docker SIGKILLs mid-drain. It returns the
// calls still live when it gave up.
func (g *gatewayRole) drain(ctx context.Context) int {
	dctx, cancel := context.WithTimeout(ctx, time.Duration(g.d.k.Int("gateway_drain_seconds"))*time.Second)
	defer cancel()
	left := g.sig.Drain(dctx)
	if left == 0 {
		g.d.log.Info("drained, shutting down")
		return 0
	}
	g.d.log.Warn("drain timed out, ending live calls", "calls", left)
	ectx, ecancel := context.WithTimeout(ctx, endCallsTimeout)
	defer ecancel()
	if left = g.sig.EndAll(ectx); left > 0 {
		g.d.log.Warn("calls still up after ending them", "calls", left)
	}
	return left
}

func (g *gatewayRole) shutdown(ctx context.Context) {
	httpCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = g.srv.Shutdown(httpCtx)
	_ = g.ctlSrv.Shutdown(httpCtx)
	g.hub.Close()
	if g.cdr != nil {
		recordingCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		if err := g.cdr.CloseRecordings(recordingCtx); err != nil {
			g.d.log.Warn("recordings left on disk for next startup", "err", err)
		}
	}
}

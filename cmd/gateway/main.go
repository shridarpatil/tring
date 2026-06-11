// Package main: Tring WebRTC media gateway (ADR-001).
//
// The browser-facing front door for web calling. Browsers peer with this
// gateway over raw WebRTC (HTTPS offer/answer signaling) instead of SIP-over-
// WSS to FreeSWITCH. The gateway terminates the browser leg and (milestone 2+)
// bridges media into a PRIVATE FreeSWITCH. Milestone 1 = loopback echo.
//
// Env: TRING_GATEWAY_ADDR, TRING_STUN_URL, TRING_TURN_URL, and either
//
//	TRING_TURN_USERNAME/_PASSWORD (static creds) or TRING_TURN_SECRET (the
//	gateway mints its own ephemeral coturn creds, lifetime TRING_TURN_TTL_SECONDS),
//	TRING_TURN_RELAY_ONLY, TRING_PUBLIC_IP, TRING_GATEWAY_UDP_MIN/_MAX.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/config"
	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/flowstore"
	"github.com/whatomate/tring/internal/gateway"
	"github.com/whatomate/tring/internal/logging"
	"github.com/whatomate/tring/internal/media/sfu"
	"github.com/whatomate/tring/internal/media/signaling"
)

func main() {
	dialTest := flag.Bool("dial-test", false, "one-shot: SIP-dial FreeSWITCH, read inbound RTP for 4s, print result, exit (headless 2a check)")
	flag.Parse()
	k := config.Load()

	logger := logging.New(k.String("log_format"), k.String("log_level"))
	slog.SetDefault(logger)

	turnURL := k.String("turn_url")
	turnSecret := k.String("turn_secret")

	var ice []gateway.ICEServer
	if stun := k.String("stun_url"); stun != "" {
		ice = append(ice, gateway.ICEServer{URLs: []string{stun}})
	}
	// Static TURN creds are the legacy fallback — added ONLY when no secret is
	// set. With a secret the gateway mints EPHEMERAL coturn creds per
	// PeerConnection (gateway.newPeerConnection). A static turn_url with no
	// username/password would make pion reject the PeerConnection outright
	// ("turn server credentials required"), so we never add a credential-less one.
	if turnURL != "" && turnSecret == "" {
		ice = append(ice, gateway.ICEServer{
			URLs:       []string{turnURL},
			Username:   k.String("turn_username"),
			Credential: k.String("turn_password"),
		})
	}

	var fs *gateway.FSConfig
	if addr := k.String("fs_sip_addr"); addr != "" {
		fs = &gateway.FSConfig{
			SIPAddr:  addr,
			AuthUser: k.String("fs_auth_user"),
			AuthPass: k.String("fs_auth_pass"),
			Target:   k.String("fs_target"),
			FlowID:   k.String("fs_flow_id"),
			AdvertIP: k.String("gateway_advert_ip"),
			SIPPort:  k.Int("gateway_sip_port"),
		}
	}

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

	gw, err := gateway.New(gateway.Config{
		ICEServers:       ice,
		UDPPortMin:       uint16(k.Int("gateway_udp_min")),
		UDPPortMax:       uint16(k.Int("gateway_udp_max")),
		PublicIP:         k.String("public_ip"),
		RelayOnly:        k.Bool("turn_relay_only"),
		TURNURL:          turnURL,
		TURNSecret:       turnSecret,
		TURNTTL:          time.Duration(k.Int("turn_ttl_seconds")) * time.Second,
		FS:               fs,
		JWTSecret:        []byte(k.String("jwt_secret")),
		RequireAuth:      k.Bool("gateway_require_auth"),
		AgentRingTimeout: time.Duration(k.Int("gateway_agent_ring_timeout")) * time.Second,
		Cache:            redisCache,
		Logger:           logger,
	})
	if err != nil {
		logger.Error("gateway init failed", "err", err)
		os.Exit(1)
	}

	if *dialTest {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		n, err := gw.TestFSLeg(ctx)
		if err != nil {
			logger.Error("dial-test failed", "err", err)
			os.Exit(1)
		}
		logger.Info("dial-test ok: freeswitch leg established", "inbound_rtp_packets_4s", n)
		return
	}

	// Pion SFU media plane (internal/media) — the N-party web-calling path, served
	// on its own /rtc signaling port alongside the legacy WebRTC↔FS bridge. Same
	// ICE/TURN/public-IP config as the bridge; shares the media UDP range.
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
	if database, dberr := db.Connect(context.Background(), k.String("dsn")); dberr != nil {
		logger.Warn("gateway DB unavailable, IVR on web calls disabled", "err", dberr)
	} else {
		flows = flowstore.New(database)
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
		AudioDir:   k.String("audio_dir"),
		Logger:     logger,
	})
	rtcAddr := k.String("rtc_addr")
	rtcSrv := &http.Server{Addr: rtcAddr, Handler: sig.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("rtc signaling listening", "addr", rtcAddr)
		if err := rtcSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("rtc serve failed", "err", err)
			os.Exit(1)
		}
	}()

	addr := k.String("gateway_addr")
	srv := &http.Server{
		Addr:              addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("gateway listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("gateway serve failed", "err", err)
		os.Exit(1)
	}
}

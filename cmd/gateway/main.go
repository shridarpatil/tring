// Package main: Tring WebRTC media gateway (ADR-001).
//
// The browser-facing front door for web calling. Browsers peer with this
// gateway over raw WebRTC (HTTPS offer/answer signaling) instead of SIP-over-
// WSS to FreeSWITCH. The gateway terminates the browser leg and (milestone 2+)
// bridges media into a PRIVATE FreeSWITCH. Milestone 1 = loopback echo.
//
// Env: TRING_GATEWAY_ADDR, TRING_STUN_URL, TRING_TURN_URL/_USERNAME/_PASSWORD,
//
//	TRING_PUBLIC_IP, TRING_GATEWAY_UDP_MIN/_MAX.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/whatomate/tring/internal/config"
	"github.com/whatomate/tring/internal/gateway"
)

func main() {
	dialTest := flag.Bool("dial-test", false, "one-shot: SIP-dial FreeSWITCH, read inbound RTP for 4s, print result, exit (headless 2a check)")
	flag.Parse()
	k := config.Load()

	var ice []gateway.ICEServer
	if stun := k.String("stun_url"); stun != "" {
		ice = append(ice, gateway.ICEServer{URLs: []string{stun}})
	}
	if turn := k.String("turn_url"); turn != "" {
		ice = append(ice, gateway.ICEServer{
			URLs:       []string{turn},
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

	gw, err := gateway.New(gateway.Config{
		ICEServers:       ice,
		UDPPortMin:       uint16(k.Int("gateway_udp_min")),
		UDPPortMax:       uint16(k.Int("gateway_udp_max")),
		PublicIP:         k.String("public_ip"),
		RelayOnly:        k.Bool("turn_relay_only"),
		FS:               fs,
		JWTSecret:        []byte(k.String("jwt_secret")),
		RequireAuth:      k.Bool("gateway_require_auth"),
		AgentRingTimeout: time.Duration(k.Int("gateway_agent_ring_timeout")) * time.Second,
	})
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	if *dialTest {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		n, err := gw.TestFSLeg(ctx)
		if err != nil {
			log.Fatalf("dial-test FAILED: %v", err)
		}
		log.Printf("dial-test OK: FreeSWITCH leg established, inbound RTP packets in 4s = %d", n)
		return
	}

	addr := k.String("gateway_addr")
	srv := &http.Server{
		Addr:              addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("tring gateway listening on %s (echo mode — milestone 1)", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("gateway serve: %v", err)
	}
}

// Package config loads Tring's runtime configuration with koanf:
// built-in defaults overlaid with TRING_* environment variables.
//
// Keys are flat and lowercase — the env var minus the TRING_ prefix:
//
//	TRING_DSN            -> "dsn"
//	TRING_ESL_ADDR       -> "esl_addr"
//	TRING_FS_BASIC_AUTH  -> "fs_basic_auth"
//
// Binaries build their own typed Config from this in main(); CLI flags
// (e.g. --dsn) take precedence by using the resolved value as the flag's
// default, so precedence is flag > env > default.
package config

import (
	"strings"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

// defaults are the dev-friendly fallbacks used when no TRING_* env var is
// set. Production overrides them via env.
var defaults = map[string]any{
	"dsn":           "postgres://tring:tring_dev@localhost:5434/tring",
	"jwt_secret":    "tring_dev_jwt_secret_change_me_in_prod",
	"fs_basic_auth": "fs:fs_dev_secret_change_me_in_prod",
	"esl_addr":      "127.0.0.1:8021",
	"esl_password":  "ClueCon",
	// Structured logging (internal/logging). format: text (logfmt, local dev) |
	// json (log shippers — logchef/Loki). level: debug|info|warn|error.
	"log_format": "text",
	"log_level":  "info",
	// STUN is OFF by default. In the gateway model (ADR-001) the gateway is a
	// public server (TRING_PUBLIC_IP), so it discovers the browser's reflexive
	// address itself — a separate STUN server is redundant (and we don't want
	// to silently call a third party like Google). Set TRING_STUN_URL only for
	// unusual topologies. TURN (TRING_TURN_*) is the real fallback for clients
	// on UDP-blocking networks.
	"stun_url": "",
	"api_addr": ":8085",
	"ivr_addr": ":8086",
	// WebRTC media gateway (ADR-001): browser-facing HTTP signaling + media.
	// gateway_public_url is the browser-reachable base URL of the gateway the
	// API hands to visitors in a mint response (override per environment).
	"gateway_public_url": "http://localhost:8087",
	"gateway_addr":       ":8087",
	"gateway_udp_min":    10000,
	"gateway_udp_max":    10100,
	// Gateway → FreeSWITCH bridge (the gateway SIP-dials a private FS).
	"gateway_advert_ip": "127.0.0.1", // IP advertised to FS for SIP+RTP
	"gateway_sip_port":  5090,
	"fs_sip_addr":       "127.0.0.1:5060",
	"fs_auth_user":      "1000",
	"fs_auth_pass":      "1234",
	"fs_target":         "9999", // fallback dialplan extension (echo) when no flow is pinned
	"fs_flow_id":        "",     // pin a WebRTC call to this IVR flow (no DID); empty → fs_target
	// Require a valid agent token on the /webrtc/offer (visitor) endpoint.
	// Default ON: /webrtc/offer requires a (backend-minted) gateway call token,
	// so token-less floods are rejected before any port/FS is allocated — the
	// safe default. Every visitor call mints a token via /v1/guest-credentials
	// (backend, API key); guests just get one with no client_ref. Set false only
	// to allow the raw anonymous path (browser posts token-less with a flow id) —
	// those calls are still flow-pinned and can't reach a trunk, but the offer
	// endpoint is then an unauthenticated resource-allocation surface.
	// The agent path is unaffected — /agent/ws ALWAYS authenticates the agent JWT.
	"gateway_require_auth": true,
	// agent_transport routes the agent's own call leg. "gateway" (default since
	// the M5 cutover) routes the leg to the media gateway as a SIP endpoint so
	// agents speak WebRTC, never SIP (ADR-001); "sip" is the legacy mode where
	// agents register to FS directly (user/<sip>) — kept for migrations but the
	// SDK no longer supports it. agent_gateway is the FreeSWITCH gateway name
	// pointing at the media gateway (freeswitch/conf/sip_profiles/external/tring-gw.xml).
	"agent_transport": "gateway",
	"agent_gateway":   "tring-gw",
	// How long (seconds) an inbound agent leg rings at the gateway before it
	// gives FreeSWITCH a timeout (the browser never accepted). ADR-001 M4b.
	"gateway_agent_ring_timeout": 45,
	// redis_addr enables the IVR flow cache (host:port). Empty = caching
	// disabled (every flow resolve reads Postgres). `make up` runs a Redis
	// container and sets TRING_REDIS_ADDR; host-run dev opts in via env.
	"redis_addr": "",
	// Tenant audio library (sound uploads for IVR prompts + hold music).
	// audio_dir is where the API writes uploaded bytes; audio_fs_dir is the
	// path prefix FreeSWITCH plays them from. They default equal because the
	// standard docker deploy bind-mounts one host dir into both the api and
	// freeswitch containers at the same path (mirrors TRING_GATEWAY_DIR). For
	// host-run `make api` against a containerised FS, override audio_dir to a
	// local path while leaving audio_fs_dir at the FS-visible path.
	"audio_dir":    "/usr/share/freeswitch/sounds/tring",
	"audio_fs_dir": "/usr/share/freeswitch/sounds/tring",
	// queue_gapless_moh: a queued caller hears one continuous (gapless) hold-music
	// playback, broken by a side ESL connection the instant an agent frees.
	// Default true. Set false to fall back to the chunked playback (a ~2s seam)
	// if the gapless break/bridge ever misbehaves on a given FreeSWITCH build.
	"queue_gapless_moh": true,
}

// Load returns a koanf instance populated with defaults, then overlaid
// with TRING_* environment variables.
func Load() *koanf.Koanf {
	k := koanf.New(".")

	// 1. Defaults.
	_ = k.Load(confmap.Provider(defaults, "."), nil)

	// 2. Environment: TRING_ESL_ADDR -> "esl_addr" (lowercase, prefix
	//    trimmed; underscores kept — keys are flat, not nested).
	_ = k.Load(env.Provider("TRING_", ".", func(s string) string {
		return strings.ToLower(strings.TrimPrefix(s, "TRING_"))
	}), nil)

	return k
}

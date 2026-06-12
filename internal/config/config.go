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

	// Embed the IANA timezone database into every binary. The Go images run on
	// distroless/static, which has no /usr/share/zoneinfo, so without this the
	// TZ env var can't resolve (time.Local silently falls back to UTC) — and the
	// IVR Timing node would evaluate business hours in the wrong zone. Imported
	// here because all three binaries import this package.
	_ "time/tzdata"

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
	// TURN auth. With turn_secret set, the API mints EPHEMERAL coturn REST-API
	// credentials per call-token request (HMAC over an expiry, secret stays
	// server-side) instead of handing the static turn_username/turn_password to
	// clients — important once creds ship inside mobile app binaries. coturn must
	// run `use-auth-secret` + `static-auth-secret=<turn_secret>`. The credential
	// LIFETIME is the tenant's max_call_seconds setting (so it covers the whole
	// call), not a global value — see internal/api iceServersFor / turnTTLFor.
	// The gateway is ALSO a TURN client when turn_secret is set: it mints its
	// own ephemeral credential per PeerConnection (lifetime = turn_ttl_seconds)
	// so it gathers a relay candidate — see internal/gateway newPeerConnection.
	"turn_secret": "",
	// Ephemeral-TURN credential lifetime for the gateway's own relay candidate
	// (the API uses the tenant's max_call_seconds instead). Must outlive the
	// longest call; 4h05m = 4h call + setup buffer.
	"turn_ttl_seconds": 14700,
	"api_addr":         ":8085",
	"ivr_addr":         ":8086",
	// WebRTC media gateway (ADR-001): browser-facing HTTP signaling + media.
	// gateway_public_url is the browser-reachable base URL of the gateway the
	// API hands to visitors in a mint response (override per environment).
	"gateway_public_url": "http://localhost:8087",
	"gateway_addr":       ":8087",
	"gateway_udp_min":    10000,
	"gateway_udp_max":    10100,
	// rtc_addr: the pion SFU media-plane signaling WebSocket (/rtc) — the new
	// N-party web-calling path (internal/media). Served by cmd/gateway alongside
	// the legacy WebRTC↔FS bridge. media_grace_seconds is the room reconnect
	// window: how long a dropped participant's slot is held for a page-refresh.
	"rtc_addr":            ":8088",
	"media_grace_seconds": 30,
	// rtc_dev_tokens mounts an UNAUTHENTICATED agent-token mint at /rtc/dev-token
	// so the local test client (examples/rtc-test.html) works with `make gateway`
	// alone. Dev only — never enable in production.
	"rtc_dev_tokens": false,
	// Brain↔edge control channel (Phase 2, docs/MEDIA_CONVERGENCE_PLAN.md). The
	// orchestrator (BRAIN) serves control_addr; the gateway (EDGE) dials
	// control_url; both gated by control_secret. All empty = disabled (the gateway
	// keeps running its own IVR until 2b/2c) — existing deploys are unaffected.
	"control_addr":   "", // orchestrator: control WS listen addr, e.g. ":8089"
	"control_url":    "", // gateway: orchestrator control URL to dial, e.g. "ws://127.0.0.1:8089/control"
	"control_secret": "", // shared secret for the control channel
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
	// agent_jitter_msec installs a FreeSWITCH jitter buffer (jitterbuffer_msec)
	// on the gateway agent leg so the agent's internet/TURN uplink — which
	// arrives jittered/reordered/lossy — is smoothed (and Opus inband-FEC made
	// usable) before it's bridged to the far party. Without it, packet jitter is
	// heard as breaks in the agent's voice at the other end. "min:max" adaptive
	// ms; "" disables (fine for agents on the same LAN as the gateway).
	"agent_jitter_msec": "60:200",
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
	// recordings_dir: where FreeSWITCH writes call recordings and the API reads
	// them back to serve. Same container path in both (a shared mount), like
	// audio_dir, so the path the orchestrator hands uuid_record is exactly what
	// the API serves from.
	"recordings_dir": "/usr/share/freeswitch/recordings/tring",
	// record_sample_rate: the recording sample rate (Hz). The gateway leg is
	// Opus@48k, so without this FreeSWITCH records at the session-native 48kHz —
	// a stereo 16-bit WAV is then ~192KB/s (~31MB for under 3 min). 8000
	// (telephony narrowband) cuts that 6x with no audible loss for voice; bump
	// to 16000 for wideband. 0 = leave at session rate.
	"record_sample_rate": 8000,
	// record_format: the recording file format / extension, which selects the
	// FreeSWITCH file handler. "ogg" (mod_sndfile → Ogg Vorbis) records
	// compressed stereo directly — ~25x smaller than WAV, both channels kept,
	// no extra module. "mp3" (mod_shout) is similar size with universal browser
	// playback (Safari/iOS can't play Ogg Vorbis). "wav" is the uncompressed
	// fallback. True Opus needs mod_opusfile baked into the image (not present).
	"record_format": "ogg",

	// S3 (or S3-compatible: MinIO, R2, …) for call recordings. When s3_bucket is
	// set, a finished recording is uploaded to S3 (date-wise key) and the local
	// spool deleted; the API serves it back from S3. Empty bucket = local only.
	// s3_endpoint empty = AWS (s3.amazonaws.com).
	"s3_bucket":     "",
	"s3_region":     "",
	"s3_endpoint":   "",
	"s3_access_key": "",
	"s3_secret_key": "",
	"s3_prefix":     "recordings",
	"s3_use_ssl":    true,
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

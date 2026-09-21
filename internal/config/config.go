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
	"api_addr": ":8085",
	// Comma-separated CIDRs of the reverse proxies in front of the API
	// (e.g. "127.0.0.1/32,10.0.0.0/8"). Only X-Forwarded-For hops inside
	// these ranges are trusted when resolving the client IP for the access
	// log. Empty (the default) = no proxy: XFF is ignored entirely so a
	// direct caller can't forge its own address. See internal/api realIP.
	"trusted_proxies": "",
	// WebRTC media gateway (ADR-001): browser-facing HTTP signaling + media.
	// gateway_public_url is the browser-reachable base URL of the gateway the
	// API hands to visitors in a mint response (override per environment).
	"gateway_public_url": "http://localhost:8087",
	"gateway_addr":       ":8087",
	"gateway_udp_min":    10000,
	"gateway_udp_max":    10100,
	// media_grace_seconds is the room reconnect window: how long a dropped
	// participant's slot is held for a page-refresh. The pion media-plane
	// signaling WebSocket (/rtc) is served by cmd/gateway on gateway_addr.
	"media_grace_seconds": 30,
	// rtc_dev_tokens mounts an UNAUTHENTICATED agent-token mint at /rtc/dev-token
	// so the local test client (examples/rtc-test.html) works with `make gateway`
	// alone. Dev only — never enable in production.
	"rtc_dev_tokens": false,
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
	// redis_addr enables the IVR flow cache (host:port). Empty = caching
	// disabled (every flow resolve reads Postgres). `make up` runs a Redis
	// container and sets TRING_REDIS_ADDR; host-run dev opts in via env.
	"redis_addr": "",
	// audio_dir is the tenant audio library (IVR prompt uploads + synthesized
	// TTS + hold music). The API writes bytes here; the gateway reads + plays
	// them on the pion media plane. A shared mount in the standard docker deploy
	// (one host dir into both the api and gateway containers at the same path).
	// FreeSWITCH no longer plays these — the gateway does — so the path is no
	// longer FS-specific.
	"audio_dir": "/var/lib/tring/sounds",
	// recordings_dir: where the gateway (internal/callcdr) writes call
	// recordings and the API reads them back to serve (GET /v1/calls/{id}/
	// recording). A shared mount, like audio_dir; FreeSWITCH no longer writes
	// recordings.
	"recordings_dir": "/var/lib/tring/recordings",
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

	// Text-to-speech (local Piper). On flow save, the API synthesizes IVR node
	// prompt text (with no uploaded audio) to 48 kHz mono Opus-Ogg (piper + the
	// bundled ffmpeg) into the sound library, and stamps the node's audio_file —
	// so both planes play it via the normal audio path with no call-time TTS. No
	// on/off flag: TTS auto-enables when the piper binary + model resolve (bundled
	// in the API image) and no-ops gracefully otherwise. The synthesized files
	// live under <audio_dir>/tts (derived), so a stock deploy needs zero TTS config.
	"tts_piper_bin":   "/opt/piper/piper",
	"tts_piper_model": "/usr/local/share/piper/voice.onnx",
	"tts_voice":       "", // speaker id for multi-speaker models ("" → default 0)

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

	// fsplug: the FreeSWITCH SIP plug (internal/fsplug), a room.Plug adapter
	// for inbound PSTN calls over FreeSWITCH. OFF by default — nothing changes
	// until a live trunk exists. fsplug_fs_sip is declared now for the later
	// outbound-leg work; unused today.
	// fsplug_sip_listen must NOT be :5080 — that's FreeSWITCH's own external
	// profile SIP port (vars.xml external_sip_port). The gateway's SIP UAS
	// convention is :5090 (vars.xml tring_gateway_sip / TRING_GATEWAY_SIP_PORT,
	// and freeswitch/conf/sip_profiles/external/tring-gw.xml proxies there) —
	// keep this in sync with TRING_GATEWAY_SIP_PORT.
	"fsplug_enabled":       false,
	"fsplug_sip_listen":    ":5090",
	"fsplug_sip_advertise": "127.0.0.1",
	"fsplug_rtp_min":       12000,
	"fsplug_rtp_max":       12100,
	"fsplug_fs_sip":        "127.0.0.1:5060",
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

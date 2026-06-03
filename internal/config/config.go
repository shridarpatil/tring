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
// set. Production overrides them via env. ws_url is intentionally absent —
// the api binary auto-detects the host's outbound IP when it's unset.
var defaults = map[string]any{
	"dsn":           "postgres://tring:tring_dev@localhost:5434/tring",
	"jwt_secret":    "tring_dev_jwt_secret_change_me_in_prod",
	"fs_basic_auth": "fs:fs_dev_secret_change_me_in_prod",
	"esl_addr":      "127.0.0.1:8021",
	"esl_password":  "ClueCon",
	"stun_url":      "stun:stun.l.google.com:19302",
	"api_addr":      ":8085",
	"ivr_addr":      ":8086",
	// WebRTC media gateway (ADR-001): browser-facing HTTP signaling + media.
	"gateway_addr":     ":8087",
	"gateway_udp_min":  10000,
	"gateway_udp_max":  10100,
	// Gateway → FreeSWITCH bridge (the gateway SIP-dials a private FS).
	"gateway_advert_ip": "127.0.0.1", // IP advertised to FS for SIP+RTP
	"gateway_sip_port":  5090,
	"fs_sip_addr":       "127.0.0.1:5060",
	"fs_auth_user":      "1000",
	"fs_auth_pass":      "1234",
	"fs_target":         "9999", // dialplan extension the gateway calls (echo for the POC)
	// redis_addr enables the IVR flow cache (host:port). Empty = caching
	// disabled (every flow resolve reads Postgres). `make up` runs a Redis
	// container and sets TRING_REDIS_ADDR; host-run dev opts in via env.
	"redis_addr": "",
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

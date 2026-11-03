package main

import (
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/knadh/koanf/v2"

	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/config"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/safehttp"
	"github.com/shridarpatil/tring/internal/secretbox"
)

// deps are built once per process and shared by both roles. The events Hub and
// the DB pool are not here: each role keeps its own (see startAPI/startGateway).
type deps struct {
	k             *koanf.Koanf
	log           *slog.Logger
	secrets       *secretbox.Box // nil when secrets_key is unset
	outboundAllow []netip.Prefix
	cache         *cache.Cache      // nil when Redis is unreachable
	pub           *events.Publisher // nil when Redis is unreachable
	blob          *blobstore.Store
}

func newDeps(k *koanf.Koanf, gatewayOnly bool) (*deps, error) {
	logger := logging.New(k.String("log_format"), k.String("log_level"))
	slog.SetDefault(logger) // so startup helpers (bootstrap) log through it too
	d := &deps{k: k, log: logger}

	if weak := config.WeakSecrets(k, weakSecretKeys(gatewayOnly)...); len(weak) > 0 && !k.Bool("allow_insecure_secrets") {
		return nil, fmt.Errorf("refusing to start with default or short secrets %v; set them in .env (or TRING_ALLOW_INSECURE_SECRETS=true for local dev)", weak)
	}

	// TRING_SECRETS_KEY seals trunk/agent SIP passwords at rest and opens sealed
	// webhook headers. Optional for now (see api.PrepareStoredSecrets for the
	// keyless upgrade path), but a set-and-malformed key is always fatal.
	if v := k.String("secrets_key"); v != "" {
		key, err := secretbox.ParseKey(v)
		if err == nil {
			d.secrets, err = secretbox.New(key)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid TRING_SECRETS_KEY: %w", err)
		}
	}

	// SSRF guard for tenant-supplied URLs (IVR http_callback, webhooks, flow
	// callbacks): public internet + the operator's outbound_allow_cidrs only.
	allow, err := safehttp.ParseCIDRs(csv(k.String("outbound_allow_cidrs")))
	if err != nil {
		return nil, fmt.Errorf("invalid outbound_allow_cidrs: %w", err)
	}
	d.outboundAllow = allow

	// Shared Redis: agent presence, single-use call tokens, the DID/flow cache
	// (the api invalidates what the gateway caches). Optional here: log-and-
	// disable if unreachable; the gateway role decides whether it can run without.
	d.cache, err = cache.New(k.String("redis_addr"), k.String("redis_password"))
	if err != nil {
		logger.Warn("redis unavailable, cache, presence and flow-cache invalidation disabled", "err", err)
	}

	// Dashboard live-update publisher. Best-effort: nil when Redis is off, and
	// events simply don't fire.
	d.pub, err = events.NewPublisher(k.String("redis_addr"), k.String("redis_password"), logger)
	if err != nil {
		logger.Warn("redis unavailable, event publishing disabled", "err", err)
	}

	// Optional S3 store for call recordings (upload on the gateway, serving on
	// the api). Misconfig → warn and use the local volume instead.
	d.blob, err = blobstore.New(blobstore.Config{
		Bucket:    k.String("s3_bucket"),
		Region:    k.String("s3_region"),
		Endpoint:  k.String("s3_endpoint"),
		AccessKey: k.String("s3_access_key"),
		SecretKey: k.String("s3_secret_key"),
		Prefix:    k.String("s3_prefix"),
		UseSSL:    k.Bool("s3_use_ssl"),
	})
	if err != nil {
		logger.Warn("S3 recording storage disabled (config error)", "err", err)
	}
	return d, nil
}

func (d *deps) close() {
	d.pub.Close()
	d.cache.Close()
}

// csv splits a comma-separated config value into trimmed, non-empty entries.
// koanf hands env vars through as plain strings, so list-valued settings
// (trusted_proxies, *_cidrs) are split here at the wiring layer.
func csv(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

package blobstore

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/secretbox"
)

// Loader returns a tenant's current S3 config (Transport is filled in by the
// Resolver).
type Loader func(ctx context.Context, tenant string) (Config, error)

// Resolver hands out each tenant's Store. The config is read on every call so
// a save in the console applies to the next recording, while the minio client
// (and its cached IAM credentials) is reused until that tenant's config changes.
type Resolver struct {
	load      Loader
	transport http.RoundTripper

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	cfg   Config // Transport left nil, so configs compare by their S3 fields
	store *Store
}

// NewResolver builds a Resolver; transport carries every tenant's S3 traffic.
func NewResolver(load Loader, transport http.RoundTripper) *Resolver {
	return &Resolver{load: load, transport: transport, cache: map[string]cached{}}
}

// For returns the tenant's Store, or nil (not an error) when the tenant has no
// bucket: recordings stay on local disk. A nil Resolver also yields nil.
func (r *Resolver) For(ctx context.Context, tenant string) (*Store, error) {
	if r == nil {
		return nil, nil
	}
	cfg, err := r.load(ctx, tenant)
	if err != nil {
		return nil, err
	}
	cfg.Transport = nil
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.cache[tenant]; ok && c.cfg == cfg {
		return c.store, nil
	}
	if cfg.Bucket == "" {
		delete(r.cache, tenant)
		return nil, nil
	}
	built := cfg
	built.Transport = r.transport
	s, err := New(built)
	if err != nil {
		return nil, err
	}
	r.cache[tenant] = cached{cfg: cfg, store: s}
	return s, nil
}

// Build makes a Store from cfg with the Resolver's transport, without caching
// it: the api tests a config this way before saving it.
func (r *Resolver) Build(cfg Config) (*Store, error) {
	cfg.Transport = r.transport
	return New(cfg)
}

// errSealedNoKey: the stored secret is sealed but this process has no
// TRING_SECRETS_KEY to open it.
var errSealedNoKey = errors.New("s3 secret is encrypted but TRING_SECRETS_KEY is not set")

// DBLoader reads a tenant's config from tenants.s3_*, opening the sealed
// secret with box (nil box: only a plaintext secret can be used).
func DBLoader(d *db.DB, q *queries.Queries, box *secretbox.Box) Loader {
	return func(ctx context.Context, tenant string) (Config, error) {
		var c Config
		if err := d.Pool.QueryRow(ctx, q.GetTenantS3, tenant).Scan(
			&c.Bucket, &c.Region, &c.Endpoint, &c.Prefix, &c.UseSSL, &c.AccessKey, &c.SecretKey,
		); err != nil {
			return Config{}, err
		}
		if c.SecretKey == "" {
			return c, nil
		}
		if box == nil {
			if secretbox.IsSealed(c.SecretKey) {
				return Config{}, errSealedNoKey
			}
			return c, nil
		}
		sk, err := box.Open(c.SecretKey)
		if err != nil {
			return Config{}, err
		}
		c.SecretKey = sk
		return c, nil
	}
}

package blobstore

import (
	"context"
	"testing"
)

func TestResolverCachesAndReplaces(t *testing.T) {
	cfg := Config{Bucket: "rec-a", Region: "ap-south-1", AccessKey: "k", SecretKey: "s", UseSSL: true}
	loads := 0
	r := NewResolver(func(_ context.Context, tenant string) (Config, error) {
		loads++
		if tenant != "t1" {
			t.Fatalf("tenant = %q", tenant)
		}
		return cfg, nil
	}, nil)
	ctx := context.Background()

	a, err := r.For(ctx, "t1")
	if err != nil || !a.Enabled() {
		t.Fatalf("For: %v, %v", a, err)
	}
	b, _ := r.For(ctx, "t1")
	if a != b {
		t.Error("same config should reuse the cached store")
	}

	cfg.Bucket = "rec-b"
	c, _ := r.For(ctx, "t1")
	if c == a || c.bucket != "rec-b" {
		t.Errorf("changed config should build a new store, got bucket %q", c.bucket)
	}

	cfg.Bucket = ""
	d, err := r.For(ctx, "t1")
	if err != nil || d != nil || d.Enabled() {
		t.Errorf("no bucket: got %v, %v; want nil store", d, err)
	}
	if loads != 4 {
		t.Errorf("loads = %d, want 4 (config is read on every call)", loads)
	}
}

func TestResolverNil(t *testing.T) {
	var r *Resolver
	s, err := r.For(context.Background(), "t1")
	if s != nil || err != nil {
		t.Errorf("nil resolver: got %v, %v", s, err)
	}
}

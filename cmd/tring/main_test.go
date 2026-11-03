package main

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/config"
)

func TestParseOptionsDefaults(t *testing.T) {
	k := config.Load()
	o, err := parseOptions(nil, k)
	if err != nil {
		t.Fatal(err)
	}
	if o.gatewayOnly {
		t.Error("gatewayOnly = true, want false")
	}
	if o.addr != k.String("api_addr") {
		t.Errorf("addr = %q, want %q", o.addr, k.String("api_addr"))
	}
	if o.dsn != k.String("dsn") {
		t.Errorf("dsn = %q, want %q", o.dsn, k.String("dsn"))
	}
}

func TestParseOptionsGateway(t *testing.T) {
	o, err := parseOptions([]string{"--gateway"}, config.Load())
	if err != nil {
		t.Fatal(err)
	}
	if !o.gatewayOnly {
		t.Error("gatewayOnly = false, want true")
	}
}

func TestParseOptionsSeedWithGatewayRejected(t *testing.T) {
	if _, err := parseOptions([]string{"--gateway", "--seed-tenant", "Acme"}, config.Load()); err == nil {
		t.Fatal("want an error: seeding is an api-role action")
	}
}

func TestWeakSecretKeys(t *testing.T) {
	if got := weakSecretKeys(true); !slices.Equal(got, []string{"jwt_secret"}) {
		t.Errorf("gateway: %v", got)
	}
	if got := weakSecretKeys(false); !slices.Equal(got, []string{"jwt_secret", "fs_basic_auth", "esl_password"}) {
		t.Errorf("all: %v", got)
	}
}

// --dsn must reach both roles: the gateway reads the DSN from config, so the
// flag has to land there too.
func TestParseOptionsDSNAppliesToConfig(t *testing.T) {
	k := config.Load()
	o, err := parseOptions([]string{"--dsn", "postgres://u:p@db.example:5432/tring_x"}, k)
	if err != nil {
		t.Fatal(err)
	}
	if k.String("dsn") != o.dsn {
		t.Errorf("config dsn = %q, want the flag's %q", k.String("dsn"), o.dsn)
	}
}

// A gateway-only node shares agent state through Redis, so it must not start
// without it; all mode still runs single-node without Redis.
func TestGatewayNeedsRedis(t *testing.T) {
	err := gatewayNeedsRedis(options{gatewayOnly: true}, false)
	if err == nil || !strings.Contains(err.Error(), "TRING_REDIS_ADDR") {
		t.Fatalf("gateway without Redis: err = %v, want one naming TRING_REDIS_ADDR", err)
	}
	if err := gatewayNeedsRedis(options{}, false); err != nil {
		t.Errorf("all mode without Redis: %v, want nil", err)
	}
	if err := gatewayNeedsRedis(options{gatewayOnly: true}, true); err != nil {
		t.Errorf("gateway with Redis: %v, want nil", err)
	}
}

// All mode keeps today's single-node behaviour when the cluster can't start
// (Redis flaky after the cache dialled); a gateway-only node has no reason to
// exist without the cluster, so it still refuses.
func TestClusterOrSingle(t *testing.T) {
	fail := errors.New("dial tcp: connection refused")
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	cl, err := clusterOrSingle("all", nil, fail, log)
	if err != nil || cl != nil {
		t.Fatalf("all mode: cl=%v err=%v, want nil, nil", cl, err)
	}
	if !strings.Contains(logs.String(), "cluster disabled, running as a single node") {
		t.Errorf("all mode: no warning logged, got %q", logs.String())
	}

	cl, err = clusterOrSingle("gateway", nil, fail, log)
	if err == nil || !errors.Is(err, fail) || cl != nil {
		t.Fatalf("gateway mode: cl=%v err=%v, want the cluster error", cl, err)
	}

	cl, err = clusterOrSingle("all", nil, nil, log)
	if err != nil || cl != nil {
		t.Fatalf("no error: cl=%v err=%v, want nil, nil", cl, err)
	}
}

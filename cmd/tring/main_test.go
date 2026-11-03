package main

import (
	"slices"
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

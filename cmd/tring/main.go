// Package main: Tring, one binary for the whole server.
//
// `tring` runs everything on this machine: the api role (REST, admin console,
// token minting, FreeSWITCH callbacks, migrations) and the gateway role (the
// /rtc call plane: pion SFU with the brain in-process). `tring --gateway` runs
// only the gateway role, for a machine that adds call capacity.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/knadh/koanf/v2"

	"github.com/shridarpatil/tring/internal/config"
)

// version is stamped at build time (-ldflags "-X main.version=…", by the
// Dockerfile and GoReleaser); a plain `go build` reports "dev".
var version = "dev"

// options are the command-line flags. Flags default to the config value, so
// flag > env > default.
type options struct {
	gatewayOnly bool
	addr        string // api listen addr (ignored with --gateway)
	dsn         string
	seedTenant  string
	showVersion bool
}

func parseOptions(args []string, k *koanf.Koanf) (options, error) {
	var o options
	fs := flag.NewFlagSet("tring", flag.ContinueOnError)
	fs.BoolVar(&o.gatewayOnly, "gateway", false, "Run only the gateway (call plane), not the api")
	fs.StringVar(&o.addr, "addr", k.String("api_addr"), "api HTTP listen address")
	fs.StringVar(&o.dsn, "dsn", k.String("dsn"), "Postgres connection string")
	fs.StringVar(&o.seedTenant, "seed-tenant", "", "If set, creates a tenant with the given name, prints the API key, then exits")
	fs.BoolVar(&o.showVersion, "version", false, "Print the version and exit")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	// Seeding writes through the api role (migrations, bootstrap); a gateway-only
	// node never touches the schema.
	if o.gatewayOnly && o.seedTenant != "" {
		return o, errors.New("--seed-tenant can't be combined with --gateway")
	}
	return o, nil
}

// weakSecretKeys lists the secrets that must not be defaults. A gateway-only
// node never serves /fs or dials ESL, so its .env may omit those two.
func weakSecretKeys(gatewayOnly bool) []string {
	if gatewayOnly {
		return []string{"jwt_secret"}
	}
	return []string{"jwt_secret", "fs_basic_auth", "esl_password"}
}

func main() {
	o, err := parseOptions(os.Args[1:], config.Load())
	if err != nil {
		os.Exit(2)
	}
	if o.showVersion {
		fmt.Println(version)
		return
	}
}

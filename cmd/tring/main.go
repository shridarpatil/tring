// Package main: Tring, one binary for the whole server.
//
// `tring` runs everything on this machine: the api role (REST, admin console,
// token minting, FreeSWITCH callbacks, migrations) and the gateway role (the
// /rtc call plane: pion SFU with the brain in-process). `tring --gateway` runs
// only the gateway role, for a machine that adds call capacity.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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

// apiStopper and gatewayStopper are what run needs from a started role; tests
// substitute fakes to pin the start and shutdown order.
type apiStopper interface{ shutdown(context.Context) }

type gatewayStopper interface {
	drain(context.Context) int
	shutdown(context.Context)
}

// roles starts each part of the process; main binds them to the shared deps.
type roles struct {
	api     func() (apiStopper, error)
	gateway func() (gatewayStopper, error)
	seed    func() error
}

// run starts the roles the options ask for and blocks until stop closes. The
// api starts first because it migrates the schema the gateway reads. Shutdown
// is the reverse: the gateway drains its live calls while the api (console,
// live dashboard) keeps serving, then the api stops last.
func run(o options, r roles, stop <-chan struct{}) error {
	if o.seedTenant != "" {
		return r.seed()
	}
	var a apiStopper
	if !o.gatewayOnly {
		var err error
		if a, err = r.api(); err != nil {
			return err
		}
	}
	g, err := r.gateway()
	if err != nil {
		if a != nil {
			a.shutdown(context.Background())
		}
		return err
	}
	<-stop
	g.drain(context.Background())
	g.shutdown(context.Background())
	if a != nil {
		a.shutdown(context.Background())
	}
	return nil
}

func main() {
	k := config.Load()
	o, err := parseOptions(os.Args[1:], k)
	if err != nil {
		os.Exit(2)
	}
	if o.showVersion {
		fmt.Println(version)
		return
	}
	d, err := newDeps(k, o.gatewayOnly)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer d.close()

	// SIGTERM (docker stop) starts the drain. A second signal exits at once:
	// stop() restores the default handler after the first.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()

	err = run(o, roles{
		api:     func() (apiStopper, error) { return startAPI(d, o) },
		gateway: func() (gatewayStopper, error) { return startGateway(d) },
		seed:    func() error { return runSeed(d, o) },
	}, ctx.Done())
	if err != nil {
		d.log.Error("tring stopped", "err", err)
		d.close()
		os.Exit(1)
	}
}

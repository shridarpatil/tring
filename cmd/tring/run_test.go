package main

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// recorder builds fake roles that log each lifecycle call in order.
type recorder struct{ calls []string }

type fakeAPI struct{ r *recorder }

func (f fakeAPI) shutdown(context.Context) { f.r.calls = append(f.r.calls, "api.shutdown") }

type fakeGateway struct{ r *recorder }

func (f fakeGateway) drain(context.Context) int {
	f.r.calls = append(f.r.calls, "gateway.drain")
	return 0
}
func (f fakeGateway) shutdown(context.Context) { f.r.calls = append(f.r.calls, "gateway.shutdown") }

func (r *recorder) roles(apiErr error) roles {
	return roles{
		api: func() (apiStopper, error) {
			r.calls = append(r.calls, "api.start")
			if apiErr != nil {
				return nil, apiErr
			}
			return fakeAPI{r}, nil
		},
		gateway: func() (gatewayStopper, error) {
			r.calls = append(r.calls, "gateway.start")
			return fakeGateway{r}, nil
		},
		seed: func() error {
			r.calls = append(r.calls, "seed")
			return nil
		},
	}
}

func stopped() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func TestShutdownOrder(t *testing.T) {
	var r recorder
	if err := run(options{}, r.roles(nil), stopped()); err != nil {
		t.Fatal(err)
	}
	want := []string{"api.start", "gateway.start", "gateway.drain", "gateway.shutdown", "api.shutdown"}
	if !slices.Equal(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestGatewayModeSkipsAPI(t *testing.T) {
	var r recorder
	if err := run(options{gatewayOnly: true}, r.roles(nil), stopped()); err != nil {
		t.Fatal(err)
	}
	want := []string{"gateway.start", "gateway.drain", "gateway.shutdown"}
	if !slices.Equal(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestSeedTenantSkipsGateway(t *testing.T) {
	var r recorder
	// A nil stop channel blocks forever: run must return without waiting on it.
	if err := run(options{seedTenant: "Acme"}, r.roles(nil), nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"seed"}; !slices.Equal(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestAPIStartFailureStopsBeforeGateway(t *testing.T) {
	var r recorder
	boom := errors.New("boom")
	if err := run(options{}, r.roles(boom), stopped()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if slices.Contains(r.calls, "gateway.start") {
		t.Errorf("gateway started after api failure: %v", r.calls)
	}
}

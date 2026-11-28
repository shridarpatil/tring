package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// newPprofServer serves Go's runtime profiles (/debug/pprof/) on addr, for
// measuring where the gateway's CPU and memory go under load. Profiles expose
// stack traces, heap contents and command-line flags, so only a loopback
// address is accepted: reach it with an SSH tunnel, never through a port that
// browsers or the internet can hit.
func newPprofServer(addr string) (*http.Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("gateway_pprof_addr %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("gateway_pprof_addr %q: must be a loopback address (127.0.0.1, ::1 or localhost)", addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: /profile and /trace stream for ?seconds=N.
	}, nil
}

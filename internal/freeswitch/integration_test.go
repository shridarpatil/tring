//go:build integration

// Run with: go test -tags=integration ./internal/freeswitch/...
//
// Requires a reachable FreeSWITCH (ESL on TRING_INTEGRATION_FS_ADDR, default
// 127.0.0.1:8021, password TRING_INTEGRATION_FS_PASSWORD or ClueCon). The
// CI workflow boots the full compose stack and points these env vars at it.

package freeswitch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func intAddr() string {
	if v := os.Getenv("TRING_INTEGRATION_FS_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:8021"
}

func intPassword() string {
	if v := os.Getenv("TRING_INTEGRATION_FS_PASSWORD"); v != "" {
		return v
	}
	return "ClueCon"
}

// TestCommandStatus runs a synchronous api command against FreeSWITCH. This
// verifies the ESL connect → auth → api command → reply chain end to end.
func TestCommandStatus(t *testing.T) {
	c := New(intAddr(), intPassword())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reply, err := c.Command(ctx, "status", "")
	if err != nil {
		t.Fatalf("Command status: %v", err)
	}
	if !strings.Contains(reply, "UP") {
		t.Errorf("status reply = %q, want it to report FreeSWITCH UP", reply)
	}
}

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

	"github.com/google/uuid"
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

// TestOriginateLoopbackEcho asks FreeSWITCH to originate a call where both
// legs are loopback dial strings into the `9196` echo extension. This
// verifies the ESL connect → bgapi originate → reply parse chain end to end
// without needing a real SIP trunk or a registered softphone.
//
// The originate is BACKGROUNDED (bgapi), so success means: connection
// established, command accepted, a Job-UUID parsed from the reply. The
// background call itself is best-effort — we don't wait for the channel.
func TestOriginateLoopbackEcho(t *testing.T) {
	c := New(intAddr(), intPassword())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	chanUUID := uuid.NewString()
	res, err := c.Originate(ctx, OriginateRequest{
		ALeg:     "loopback/9196/default",
		BLegs:    []string{"loopback/9196/default"},
		UUID:     chanUUID,
		CallerID: "+918000000000",
		Vars: map[string]string{
			"hangup_after_bridge": "true",
			"ignore_early_media":  "true",
		},
	})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	if res.ChannelUUID != chanUUID {
		t.Errorf("ChannelUUID = %q, want pre-allocated %q", res.ChannelUUID, chanUUID)
	}
	if res.JobUUID == "" {
		t.Errorf("JobUUID empty — Reply-Text parsing broke; was: %+v", res)
	}
	// Sanity: Job-UUIDs from FreeSWITCH are UUID-shaped.
	if !strings.Contains(res.JobUUID, "-") {
		t.Errorf("JobUUID %q doesn't look like a UUID", res.JobUUID)
	}
}

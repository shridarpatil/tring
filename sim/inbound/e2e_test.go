// Package inbound holds a gated, opt-in end-to-end test for the
// FreeSWITCH inbound SIP plug (internal/fsplug). It is NOT part of the
// normal `go test ./...` run in any meaningful sense — it requires a
// live FreeSWITCH + gateway stack, a seeded tenant/DID/flow, and the
// `sipp` binary on PATH — so it skips instantly unless explicitly
// opted into via environment variables. See README.md for the full
// manual run recipe.
package inbound

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestInboundE2E drives a real SIPp UAC (invite.xml) against a live
// FreeSWITCH external SIP profile, impersonating a carrier trunk
// sending a PCMU offer. If the stack is wired correctly, FreeSWITCH's
// `public` dialplan (freeswitch/conf/dialplan/public.xml) forces
// absolute_codec_string=opus and bridges into the gateway's fsplug SIP
// UAS (:5090 by default), which answers, resolves the dialed DID to a
// flow via room.Plug, and runs the call on the pion media plane. That
// proves the PCMU(carrier)->Opus(plug) transcode path end to end —
// something the loopback smoke test (sim/sipp-uas.xml) cannot show.
//
// Gated behind two independent env vars so it never runs by accident:
//   - TRING_E2E=1                      opts into the whole gated suite
//   - TRING_FS_EXTERNAL=<host:port>    FreeSWITCH's external SIP bind,
//     e.g. 127.0.0.1:5080
//   - TRING_TEST_DSN=<postgres dsn>    used only to assert the landed
//     CDR row; same convention as internal/dbtest, but this test does
//     NOT truncate anything (it's asserting against a running stack,
//     not a disposable fixture DB)
//
// The dialed DID defaults to 919000000001 (matches the design brief);
// override with TRING_E2E_DID if your seeded tenant uses a different
// inbound number.
func TestInboundE2E(t *testing.T) {
	if os.Getenv("TRING_E2E") != "1" {
		t.Skip("set TRING_E2E=1 to run the FreeSWITCH<->gateway inbound e2e (see README.md)")
	}
	fsExternal := os.Getenv("TRING_FS_EXTERNAL")
	if fsExternal == "" {
		t.Skip("set TRING_FS_EXTERNAL to FreeSWITCH's external SIP ip:port (e.g. 127.0.0.1:5080)")
	}
	dsn := os.Getenv("TRING_TEST_DSN")
	if dsn == "" {
		t.Skip("set TRING_TEST_DSN to a Postgres DSN so the landed CDR can be asserted")
	}
	if _, err := exec.LookPath("sipp"); err != nil {
		t.Skip("sipp not found on PATH; install sip-tester (see README.md)")
	}

	did := os.Getenv("TRING_E2E_DID")
	if did == "" {
		did = "919000000001"
	}

	scenario, err := filepath.Abs("invite.xml")
	if err != nil {
		t.Fatalf("resolve scenario path: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	before := time.Now().Add(-2 * time.Second)

	cmd := exec.CommandContext(ctx, "sipp",
		"-sf", scenario,
		"-s", did,
		"-m", "1",
		"-timeout", "20s",
		fsExternal,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sipp run failed: %v\n%s", err, out)
	}
	t.Logf("sipp output:\n%s", out)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to %s: %v", dsn, err)
	}
	defer pool.Close()

	var (
		id        string
		source    string
		startedAt time.Time
	)
	row := pool.QueryRow(ctx, `
		SELECT id, source, started_at
		FROM calls
		WHERE source = 'phone' AND started_at >= $1
		ORDER BY started_at DESC
		LIMIT 1
	`, before)
	if err := row.Scan(&id, &source, &startedAt); err != nil {
		t.Fatalf("no phone-source calls row landed after inbound SIPp run (dialed DID %s): %v", did, err)
	}

	t.Logf("inbound e2e: call id=%s source=%s started_at=%s", id, source, startedAt)
}

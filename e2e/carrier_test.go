//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// TestCarrierAnswersOptions: FreeSWITCH pings an IP-auth trunk with OPTIONS
// every 25 s and marks it down if unanswered. The trunk only reaches "online"
// once a ping got a 200, so this pins the fake carrier answering them.
func TestCarrierAnswersOptions(t *testing.T) {
	api := requireStack(t)
	c := NewCarrier(t, carrierIP(t))
	trunk := api.CreateTrunk(uniq("carrier-ping"), c.Addr())
	api.WaitTrunkUsable(trunk)
	api.waitFor(40*time.Second, "trunk online (an OPTIONS ping answered)", func() bool {
		return api.TrunkStatus(trunk) == "online"
	})
}

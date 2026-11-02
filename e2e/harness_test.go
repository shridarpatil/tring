//go:build e2e

package e2e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Each live() attempt runs in its own scope: what an attempt set up (agents,
// carrier legs) is torn down before the next attempt starts.
func TestLiveRetryTearsDownEachAttempt(t *testing.T) {
	var cleaned []int
	attempt := 0
	live(t, func(t *testing.T) error {
		attempt++
		n := attempt
		t.Cleanup(func() { cleaned = append(cleaned, n) })
		if n == 1 {
			if len(cleaned) != 0 {
				t.Errorf("attempt 1 started with cleanups already run: %v", cleaned)
			}
			return errNotLive
		}
		if len(cleaned) != 1 || cleaned[0] != 1 {
			t.Errorf("attempt 2 started before attempt 1 was torn down (cleaned %v)", cleaned)
		}
		return nil
	})
}

// The carrier is one SIP endpoint for the whole run, so FreeSWITCH's trunk
// pings get answered between tests too, and a retry can use it again.
func TestCarrierAnswersOptionsBetweenTests(t *testing.T) {
	ip := carrierIP(t)
	t.Run("a test using the carrier", func(t *testing.T) { TheCarrier(t).OnNumber("+910000000000", FreqCustomer) })
	t.Run("another test using it again", func(t *testing.T) { TheCarrier(t).OnNumber("+910000000000", FreqCustomer) })

	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname(ip))
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(ip))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := client.Do(ctx, sip.NewRequest(sip.OPTIONS, sip.Uri{Host: ip, Port: carrierSIPPort}))
	if err != nil || res.StatusCode != sip.StatusOK {
		t.Fatalf("OPTIONS after the tests ended: %v, %v; want 200", res, err)
	}
}

// A background goroutine reporting after its test ended must not panic the
// run ("Log in goroutine after Test… has completed").
func TestReporterAfterTestEnded(t *testing.T) {
	var rep func(string, ...any)
	t.Run("short-lived", func(t *testing.T) { rep = reporter(t).Errorf })
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reporting after the test ended panicked: %v", r)
		}
	}()
	rep("late error %v", errors.New("boom"))
}

//go:build e2e

package e2e

import (
	"sync"
	"testing"
)

// report is a test's Errorf/Logf for background goroutines (SIP handlers, RTP
// and signalling loops): once the test has ended it does nothing, instead of
// panicking the whole run with "Log in goroutine after Test… has completed".
type report struct {
	t    *testing.T
	mu   sync.Mutex
	done bool
}

// reporter returns t's guarded reporter. t's cleanup waits for a report in
// progress, so none can land after the test completes.
func reporter(t *testing.T) *report {
	r := &report{t: t}
	t.Cleanup(func() {
		r.mu.Lock()
		r.done = true
		r.mu.Unlock()
	})
	return r
}

func (r *report) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.done {
		r.t.Errorf(format, args...)
	}
}

func (r *report) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.done {
		r.t.Logf(format, args...)
	}
}

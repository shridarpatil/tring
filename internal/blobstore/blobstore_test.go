package blobstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/safehttp"
)

// IAM-role credentials must be fetched with their own client: left nil,
// minio falls back to the store's HTTP client, whose SSRF-guarded dialer
// refuses the metadata address (169.254.169.254), so IAM mode never works.
func TestIAMProviderHasOwnClient(t *testing.T) {
	p := iamProvider()
	if p.Client == nil || p.Client.Transport == nil {
		t.Fatal("IAM provider has no client of its own")
	}
}

// The S3 transport keeps minio's defaults (header timeout, no transparent
// gzip decoding) and only swaps in the guarded dialer; proxy env is off so a
// proxy can't resolve the endpoint out of the guard's sight.
func TestS3TransportKeepsMinioDefaults(t *testing.T) {
	errDial := errors.New("guarded")
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errDial }
	tr, err := s3Transport(true, dial)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil || !tr.DisableCompression || tr.ResponseHeaderTimeout != time.Minute {
		t.Errorf("proxy=%v compression-off=%v header-timeout=%v", tr.Proxy != nil, tr.DisableCompression, tr.ResponseHeaderTimeout)
	}
	if _, err := tr.DialContext(context.Background(), "tcp", "s3.amazonaws.com:443"); !errors.Is(err, errDial) {
		t.Errorf("dial not the guarded one: %v", err)
	}
}

// An endpoint blocked by the dial guard says how to allow it, rather than a
// bare "could not reach".
func TestDescribeBlocked(t *testing.T) {
	err := fmt.Errorf("put: %w", &net.OpError{Op: "dial", Err: safehttp.ErrBlocked})
	if got := Describe(err); !strings.Contains(got, "TRING_OUTBOUND_ALLOW_CIDRS") {
		t.Errorf("Describe = %q", got)
	}
}

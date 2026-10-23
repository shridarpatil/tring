// Package safehttp builds the HTTP client Tring uses for requests to
// tenant-supplied URLs (IVR http_callback nodes, webhook deliveries), so a
// tenant can't aim the server at its own network: cloud metadata
// (169.254.169.254 → IAM credentials), loopback services (ESL, Redis,
// Postgres, the api's /fs listener) or anything else on a private range.
//
// The destination is checked at connect time, on the IP the dialer actually
// connects to (net.Dialer.Control), so DNS rebinding and redirects can't slip
// past it — every redirect hop dials through the same guard. ValidateURL is a
// cheaper up-front check for create-time 400s; it is NOT a substitute for the
// dial-time guard (a hostname can resolve anywhere later).
//
// Self-hosters whose CRM sits on a private network opt specific ranges back in
// with an allowlist of CIDRs (TRING_OUTBOUND_ALLOW_CIDRS). The allowlist
// overrides every block, loopback and metadata included, so list only what
// the integration needs.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is the dial error for a destination outside the public internet.
var ErrBlocked = errors.New("destination address not allowed")

// maxRedirects caps how many redirect hops the client follows.
const maxRedirects = 3

// blockedPrefixes are the ranges not covered by netip's Is* predicates.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),         // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),     // CGNAT (RFC 6598)
	netip.MustParsePrefix("240.0.0.0/4"),       // reserved + 255.255.255.255 broadcast
	netip.MustParsePrefix("fd00:ec2::254/128"), // AWS IMDS over IPv6 (also inside fc00::/7)
}

// Blocked reports whether a connection to addr must be refused: loopback,
// private (RFC 1918, fc00::/7), link-local (incl. metadata), unspecified,
// multicast, CGNAT, 0.0.0.0/8 and reserved space — in v4 or v4-mapped v6 form —
// unless addr falls inside one of the allow prefixes.
func Blocked(addr netip.Addr, allow []netip.Prefix) bool {
	if !addr.IsValid() {
		return true
	}
	a := addr.WithZone("").Unmap()
	for _, p := range allow {
		if p.Contains(a) {
			return false
		}
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() ||
		a.IsMulticast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ParseCIDRs parses allowlist entries ("10.0.0.0/8", or a bare IP as a
// single-host prefix). Any malformed entry is an error.
func ParseCIDRs(entries []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("safehttp: invalid CIDR %q", e)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// errInvalidURL is deliberately generic — it's surfaced to API callers.
var errInvalidURL = errors.New("url must be a public http or https URL")

// ValidateURL is the create-time check for a tenant-supplied URL: http/https
// with a host, and not a literal IP (or "localhost") in a blocked range. The
// dial-time guard in NewClient still applies at request time.
func ValidateURL(raw string, allow []netip.Prefix) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errInvalidURL
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		host = "127.0.0.1"
	}
	if a, err := netip.ParseAddr(host); err == nil && Blocked(a, allow) {
		return errInvalidURL
	}
	return nil
}

// NewClient returns an *http.Client that refuses to connect to blocked
// addresses (see Blocked), follows at most maxRedirects redirects (each
// re-dialed through the guard), ignores proxy env vars (a proxy would resolve
// the target out of our sight) and gives up after timeout.
func NewClient(allow []netip.Prefix, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || Blocked(ap.Addr(), allow) {
				return ErrBlocked
			}
			return nil
		},
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = dialer.DialContext
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errInvalidURL
			}
			return nil
		},
	}
}

package safehttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestBlocked(t *testing.T) {
	cases := map[string]bool{
		// blocked
		"127.0.0.1":              true,
		"127.8.9.10":             true,
		"10.1.2.3":               true,
		"172.16.0.1":             true,
		"192.168.1.1":            true,
		"169.254.169.254":        true, // cloud metadata
		"100.64.0.1":             true, // CGNAT
		"100.127.255.254":        true,
		"0.0.0.0":                true,
		"0.1.2.3":                true,
		"224.0.0.1":              true,
		"255.255.255.255":        true,
		"::1":                    true,
		"::":                     true,
		"fe80::1":                true,
		"fc00::1":                true,
		"fd00:ec2::254":          true, // AWS IMDS over IPv6
		"ff02::1":                true,
		"::ffff:127.0.0.1":       true, // v4-mapped forms
		"::ffff:169.254.169.254": true,
		"::ffff:10.0.0.1":        true,
		"::ffff:100.64.0.1":      true,
		// allowed
		"8.8.8.8":              false,
		"1.1.1.1":              false,
		"100.128.0.1":          false, // just past CGNAT
		"172.32.0.1":           false, // just past 172.16/12
		"2606:4700:4700::1111": false,
		"::ffff:8.8.8.8":       false,
	}
	for s, want := range cases {
		if got := Blocked(netip.MustParseAddr(s), nil); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", s, got, want)
		}
	}
	if !Blocked(netip.Addr{}, nil) {
		t.Error("zero Addr must be blocked")
	}
}

func TestBlockedAllowlistOverride(t *testing.T) {
	allow, err := ParseCIDRs([]string{"10.20.0.0/16", "127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	for s, want := range map[string]bool{
		"10.20.3.4":        false, // allowlisted
		"::ffff:10.20.3.4": false, // mapped form of an allowlisted v4
		"10.21.0.1":        true,  // outside the allowlisted range
		"127.0.0.1":        false,
		"127.0.0.2":        true,
		"169.254.169.254":  true, // metadata stays blocked unless listed
	} {
		if got := Blocked(netip.MustParseAddr(s), allow); got != want {
			t.Errorf("Blocked(%s, allow) = %v, want %v", s, got, want)
		}
	}
}

func TestParseCIDRs(t *testing.T) {
	if _, err := ParseCIDRs([]string{"not-a-cidr"}); err == nil {
		t.Fatal("want error for a bad CIDR")
	}
	p, err := ParseCIDRs([]string{"10.0.0.1"}) // bare IP → /32
	if err != nil || len(p) != 1 || p[0] != netip.MustParsePrefix("10.0.0.1/32") {
		t.Fatalf("bare IP: got %v, %v", p, err)
	}
}

func TestValidateURL(t *testing.T) {
	bad := []string{
		"http://127.0.0.1:8021",
		"http://169.254.169.254/",
		"http://localhost/",
		"http://LOCALHOST:6379/",
		"http://foo.localhost/",
		"http://[::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://10.0.0.1/hook",
		"http://0.0.0.0/",
		"ftp://x",
		"file:///etc/passwd",
		"http://",
		"not a url",
		"",
	}
	for _, u := range bad {
		if err := ValidateURL(u, nil); err == nil {
			t.Errorf("ValidateURL(%q) = nil, want error", u)
		}
	}
	good := []string{
		"https://example.com/hook",
		"http://example.com:8080/x?y=1",
		"https://8.8.8.8/hook",
	}
	for _, u := range good {
		if err := ValidateURL(u, nil); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", u, err)
		}
	}
	allow, _ := ParseCIDRs([]string{"10.0.0.0/8"})
	if err := ValidateURL("http://10.0.0.1/hook", allow); err != nil {
		t.Errorf("allowlisted private IP rejected: %v", err)
	}
}

func okServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClientRefusesLoopback(t *testing.T) {
	srv := okServer(t)
	resp, err := NewClient(nil, 5*time.Second).Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("safe client connected to 127.0.0.1")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
}

func TestClientAllowlistPermitsLoopback(t *testing.T) {
	srv := okServer(t)
	allow, _ := ParseCIDRs([]string{"127.0.0.1/32"})
	resp, err := NewClient(allow, 5*time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("allowlisted loopback refused: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// A redirect from an allowed destination to a blocked one is refused: every
// hop dials through the same guarded dialer.
func TestClientRefusesRedirectToBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	allow, _ := ParseCIDRs([]string{"127.0.0.1/32"}) // the redirecting server is "allowed"
	resp, err := NewClient(allow, 5*time.Second).Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("followed a redirect to the metadata address")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
}

func TestClientCapsRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	allow, _ := ParseCIDRs([]string{"127.0.0.1/32"})
	resp, err := NewClient(allow, 5*time.Second).Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect loop not capped")
	}
}

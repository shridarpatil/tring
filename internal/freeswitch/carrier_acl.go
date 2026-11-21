package freeswitch

// carrier_acl.go writes the addresses carriers may send inbound calls from,
// as set on trunks in the console, for FreeSWITCH's "carriers" ACL. The file
// sits next to the gateway XMLs (the one dir both containers share); the
// freeswitch image's tring-carrier-acl script turns it into ACL nodes, and
// RescanExternalProfile reloads the ACL so a change applies without a restart.

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CarrierACLFile is the file name inside GatewayDir. Not *.xml: external.xml
// includes every dynamic/*.xml into the profile's gateways.
const CarrierACLFile = "carriers.acl"

// ParseCIDRs parses a comma-separated list of IPs and CIDRs (a bare IP is a
// single host) into canonical prefixes. A match-all prefix (/0) is refused:
// it would switch the ACL off for every trunk while looking like a setting.
func ParseCIDRs(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for f := range strings.SplitSeq(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		var p netip.Prefix
		if strings.Contains(f, "/") {
			var err error
			if p, err = netip.ParsePrefix(f); err != nil {
				return nil, fmt.Errorf("%q is not an IP or CIDR", f)
			}
		} else {
			a, err := netip.ParseAddr(f)
			if err != nil || a.Zone() != "" {
				return nil, fmt.Errorf("%q is not an IP or CIDR", f)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("%q would allow every address", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// WriteCarrierACL replaces the carrier ACL file with prefixes, one per line,
// sorted and de-duplicated so an unchanged set leaves the file byte-identical.
// An empty set writes an empty file, which the script reads as "accept any
// source" — the behaviour before any trunk has an address set.
func WriteCarrierACL(prefixes []netip.Prefix) error {
	lines := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		lines = append(lines, p.String())
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	var body bytes.Buffer
	for _, l := range lines {
		body.WriteString(l)
		body.WriteByte('\n')
	}
	if err := os.MkdirAll(GatewayDir, 0o755); err != nil {
		return fmt.Errorf("mkdir gateway dir: %w", err)
	}
	path := filepath.Join(GatewayDir, CarrierACLFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/shridarpatil/tring/internal/freeswitch"
)

// Trunk fields are rendered into FreeSWITCH gateway XML, where $${global} and
// ${var} are expanded — a from_domain of $${tring_esl_password} would leak
// that secret to the tenant's proxy (freeswitch.ErrUnsafeValue is the render
// backstop). Hosts are restricted to host[:port] / sip-URI characters and
// usernames to SIP user characters; a password may contain anything except
// variable syntax and control characters.
var (
	trunkHostRe = regexp.MustCompile(`^[A-Za-z0-9._:\[\];=-]{0,253}$`)
	trunkUserRe = regexp.MustCompile(`^[A-Za-z0-9._@+-]{0,128}$`)
)

func validTrunkPassword(s string) bool {
	return !strings.Contains(s, "${") && strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

// validateTrunkFields checks the trunk fields that reach FreeSWITCH; empty
// values (unset) are fine.
func validateTrunkFields(username, password, realm, proxy string, extra json.RawMessage) error {
	if !trunkUserRe.MatchString(username) {
		return errors.New("sip_username may only contain letters, digits and . _ @ + -")
	}
	if !validTrunkPassword(password) {
		return errors.New("sip_password may not contain ${ or control characters")
	}
	if !trunkHostRe.MatchString(realm) || !trunkHostRe.MatchString(proxy) {
		return errors.New("sip_realm and proxy_ip must be a host or IP, optionally with :port")
	}
	if len(extra) > 0 {
		var e struct {
			FromDomain   string `json:"from_domain"`
			AuthUsername string `json:"auth_username"`
		}
		if json.Unmarshal(extra, &e) == nil {
			if !trunkHostRe.MatchString(e.FromDomain) {
				return errors.New("extra_params.from_domain must be a host or IP")
			}
			if !trunkUserRe.MatchString(e.AuthUsername) {
				return errors.New("extra_params.auth_username may only contain letters, digits and . _ @ + -")
			}
		}
	}
	return nil
}

// normalizeCIDRs validates auth_acl_cidr — the comma-separated addresses the
// trunk's carrier sends SIP from, which feed FreeSWITCH's inbound ACL — and
// returns it canonical ("10.0.0.5" → "10.0.0.5/32"), so the stored value is
// what gets enforced. Empty is fine (no restriction from this trunk).
func normalizeCIDRs(s string) (string, error) {
	ps, err := freeswitch.ParseCIDRs(s)
	if err != nil {
		return "", fmt.Errorf("auth_acl_cidr: %w", err)
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return strings.Join(out, ","), nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

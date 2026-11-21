package api

import (
	"encoding/json"
	"testing"
)

func TestValidateTrunkFields(t *testing.T) {
	ok := []struct {
		user, pw, realm, proxy, extra string
	}{
		{"", "", "", "", ""},
		{"+912240000000", "pa$$w0rd!#%", "ims.example.net", "192.0.2.35:5060", `{"from_domain":"198.51.100.62","auth_username":"+912240000000@ims.example.net"}`},
		{"u", "p", "[2001:db8::1]:5060", "sip:carrier.example.com;transport=tcp", ""},
	}
	for _, c := range ok {
		if err := validateTrunkFields(c.user, c.pw, c.realm, c.proxy, json.RawMessage(c.extra)); err != nil {
			t.Errorf("valid %+v rejected: %v", c, err)
		}
	}
	bad := []struct {
		user, pw, realm, proxy, extra string
	}{
		{"", "", "", "", `{"from_domain":"$${tring_esl_password}"}`},
		{"", "x${tring_esl_password}", "", "", ""},
		{"", "", "${sip_from_host}", "", ""},
		{"", "", "", "1.2.3.4\"/><param", ""},
		{"a b", "", "", "", ""},
		{"", "a\nb", "", "", ""},
		{"", "", "", "", `{"auth_username":"x{y}"}`},
	}
	for _, c := range bad {
		if err := validateTrunkFields(c.user, c.pw, c.realm, c.proxy, json.RawMessage(c.extra)); err == nil {
			t.Errorf("invalid %+v accepted", c)
		}
	}
}

// auth_acl_cidr is stored canonical, as the ACL will enforce it.
func TestNormalizeCIDRs(t *testing.T) {
	got, err := normalizeCIDRs("192.0.2.35, 192.0.2.33/29")
	if err != nil || got != "192.0.2.35/32,192.0.2.32/29" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := normalizeCIDRs(""); err != nil || got != "" {
		t.Errorf("empty: got %q, %v", got, err)
	}
	if _, err := normalizeCIDRs("0.0.0.0/0"); err == nil {
		t.Error("match-all accepted")
	}
}

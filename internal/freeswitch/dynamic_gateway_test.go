package freeswitch

import (
	"strings"
	"testing"
)

func TestRenderGatewayFromDomain(t *testing.T) {
	out, err := renderGateway(Gateway{Name: "t_x", ProxyIP: "198.51.100.20", FromDomain: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `<param name="from-domain" value="192.0.2.10"></param>`; !strings.Contains(string(out), want) {
		t.Errorf("missing %s in:\n%s", want, out)
	}
}

func TestRenderGatewayOmitsEmptyFromDomain(t *testing.T) {
	out, err := renderGateway(Gateway{Name: "t_x", ProxyIP: "198.51.100.20"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "from-domain") {
		t.Errorf("unexpected from-domain in:\n%s", out)
	}
}

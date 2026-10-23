package freeswitch

// dynamic_gateway.go renders SIP gateways from the DB to disk and tells
// FreeSWITCH to rescan. Gateway XML files live in
//   freeswitch/conf/sip_profiles/external/dynamic/t_<id>.xml
// which the external sofia profile picks up via an X-PRE-PROCESS include
// in external.xml. After writing, RescanExternalProfile() asks sofia to
// reload — zero-downtime gateway updates from the admin console.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
)

// Gateway is the rendering-shape for a SIP gateway — the cross-cutting
// fields needed to produce a valid <gateway> XML block, regardless of
// whether auth is REGISTER-based or IP-based.
type Gateway struct {
	ID           string // raw trunk UUID for filename
	Name         string // sofia gateway name (must be unique within profile)
	Active       bool
	Register     bool   // true = REGISTER auth; false = IP-auth (Tata-style)
	Username     string // register mode: the identity registered (IMS IMPU user part)
	AuthUsername string // register mode: digest auth username when it differs (IMS IMPI); "" = Username
	Password     string // register mode
	Realm        string // register mode + IP-mode (used as both realm and the contact host)
	ProxyIP      string // where to send: the SBC (IP-auth), or the registrar when Realm isn't resolvable (register)
	AuthACLCIDR  string // IP-auth mode: SBC /29 to trust inbound from (documentary; ACL is configured separately)
	FromDomain   string // optional sofia from-domain: host part of From on calls + OPTIONS pings (empty = omit)
}

// GatewayName builds a sofia-safe gateway name from a trunk UUID. The
// result is < 16 chars (sofia limit) and stable per trunk.
func GatewayName(id uuid.UUID) string {
	s := strings.ReplaceAll(id.String(), "-", "")
	if len(s) > 12 {
		s = s[:12]
	}
	return "t_" + s
}

// GatewayDir is where dynamic gateway XML files live. Both the api
// container (writes) and the fs container (reads) must bind-mount the
// SAME host directory at this path.
//
// Deployment wiring (docker-compose.yml — TODO when going live):
//
//	services:
//	  freeswitch:
//	    volumes:
//	      - ./freeswitch/conf/sip_profiles/external/dynamic:/etc/freeswitch/sip_profiles/external/dynamic:rw
//	  api:
//	    volumes:
//	      - ./freeswitch/conf/sip_profiles/external/dynamic:/etc/freeswitch/sip_profiles/external/dynamic:rw
//
// AND the FreeSWITCH external sofia profile must include the dir:
//
//	<gateways>
//	  <X-PRE-PROCESS cmd="include" data="external/*.xml"/>
//	  <X-PRE-PROCESS cmd="include" data="external/dynamic/*.xml"/>
//	</gateways>
//
// (The safarov default external.xml only matches "external/*.xml" — not
// the subdir — so a custom external.xml mount is required to pick up
// dynamic/*.xml. Override via env if you mount elsewhere.)
var GatewayDir = func() string {
	if v := os.Getenv("TRING_GATEWAY_DIR"); v != "" {
		return v
	}
	return "/etc/freeswitch/sip_profiles/external/dynamic"
}()

// xmlGateway is the on-disk shape rendered to a <gateway>...</gateway>
// block inside an <include> root. Sofia loads from a single <gateway>
// per file in the external profile's dynamic include directory.
type xmlGatewayParam struct {
	XMLName xml.Name `xml:"param"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:"value,attr"`
}

type xmlGateway struct {
	XMLName xml.Name          `xml:"gateway"`
	Name    string            `xml:"name,attr"`
	Params  []xmlGatewayParam `xml:"param"`
}

type xmlInclude struct {
	XMLName xml.Name   `xml:"include"`
	Gateway xmlGateway `xml:"gateway"`
}

// ErrUnsafeValue rejects a gateway field FreeSWITCH would expand: it
// substitutes $${global} and ${var} inside gateway XML, so a tenant-set
// from-domain of $${tring_fs_basic_auth} would send that secret to the
// tenant's proxy in every SIP request. Control characters are refused too.
var ErrUnsafeValue = errors.New("gateway field contains FreeSWITCH variable syntax or control characters")

// unsafeValue reports whether v would be expanded or misparsed by FreeSWITCH.
func unsafeValue(v string) bool {
	return strings.Contains(v, "${") || strings.IndexFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// renderGateway turns a Gateway into a complete <include><gateway/></include>
// XML document suitable for sofia's per-profile gateway dir.
func renderGateway(g Gateway) ([]byte, error) {
	for _, v := range []string{g.Username, g.AuthUsername, g.Password, g.Realm, g.ProxyIP, g.FromDomain} {
		if unsafeValue(v) {
			return nil, ErrUnsafeValue
		}
	}
	params := []xmlGatewayParam{}
	if g.Register {
		// No realm = register to the proxy as the domain too, as IP mode does
		// and as Tata's SIP-over-AWS guide configures it (proxy + username +
		// password, no realm). Rendering an empty realm would leave sofia
		// without a domain for the REGISTER.
		realm := g.Realm
		if realm == "" {
			realm = g.ProxyIP
		}
		params = append(params,
			xmlGatewayParam{Name: "username", Value: g.Username},
			xmlGatewayParam{Name: "password", Value: g.Password},
			xmlGatewayParam{Name: "realm", Value: realm},
		)
		// IMS carriers (Tata) register an identity (IMPU) but authenticate a
		// separate private user (IMPI).
		if g.AuthUsername != "" {
			params = append(params, xmlGatewayParam{Name: "auth-username", Value: g.AuthUsername})
		}
		// Realm is the SIP domain (e.g. a carrier's IMS domain) and is often
		// not resolvable from the circuit; proxy sends REGISTER and calls to
		// the SBC instead (register-proxy defaults to it).
		if g.ProxyIP != "" {
			params = append(params, xmlGatewayParam{Name: "proxy", Value: g.ProxyIP})
		}
		params = append(params,
			xmlGatewayParam{Name: "register", Value: "true"},
			xmlGatewayParam{Name: "expire-seconds", Value: "600"},
			xmlGatewayParam{Name: "retry-seconds", Value: "30"},
		)
	} else {
		// IP-auth: no REGISTER, no credentials. proxy_ip is where we
		// send to; realm/realm-host defaults to the proxy if unset.
		realm := g.Realm
		if realm == "" {
			realm = g.ProxyIP
		}
		params = append(params,
			xmlGatewayParam{Name: "register", Value: "false"},
			xmlGatewayParam{Name: "proxy", Value: g.ProxyIP},
			xmlGatewayParam{Name: "realm", Value: realm},
		)
	}
	// From host for carriers that expect our circuit IP rather than their
	// own realm (Tata). The From USER stays per-call: caller-id-in-from below
	// puts each call's caller-ID DID there, so no trunk-level from-user.
	if g.FromDomain != "" {
		params = append(params, xmlGatewayParam{Name: "from-domain", Value: g.FromDomain})
	}
	// SIP OPTIONS keepalive — sofia sends OPTIONS every N seconds; if
	// the peer doesn't reply, the gateway state moves to FAIL_WAIT and
	// the UI badge flips to offline. Without this, IP-auth gateways
	// stay NOREG forever regardless of reachability.
	params = append(params,
		xmlGatewayParam{Name: "ping", Value: "25"},
		xmlGatewayParam{Name: "caller-id-in-from", Value: "true"},
	)

	doc := xmlInclude{Gateway: xmlGateway{Name: g.Name, Params: params}}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// WriteGateways renders the given list to GatewayDir, replacing any
// stale files for trunks no longer in the input. Atomic per file: writes
// to .tmp then renames so a partial write can't leave half-finished XML
// for sofia to choke on.
//
// It returns the gateway names whose XML changed or was removed. sofia's
// rescan only ADDS gateways — an already-loaded one is skipped ("Ignoring
// duplicate gateway") and keeps its old settings, and a removed file never
// unloads it — so the caller must killgw these before rescanning (see
// RescanExternalProfile). Brand-new and unchanged gateways aren't listed.
func WriteGateways(gws []Gateway) (stale []string, err error) {
	if err := os.MkdirAll(GatewayDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir gateway dir: %w", err)
	}
	want := make(map[string]bool, len(gws))
	for _, g := range gws {
		path := filepath.Join(GatewayDir, "t_"+g.ID+".xml")
		body, err := renderGateway(g)
		if errors.Is(err, ErrUnsafeValue) {
			// Skip just this trunk (the API rejects such values; this catches
			// rows stored before it did). Not in `want`, so an older file for it
			// is swept and the gateway unloaded below.
			slog.Error("trunk not rendered: unsafe gateway field", "component", "freeswitch", "gateway", g.Name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("render gateway %s: %w", g.Name, err)
		}
		want[filepath.Base(path)] = true
		if old, err := os.ReadFile(path); err == nil {
			if bytes.Equal(old, body) {
				continue // unchanged: leave the loaded gateway alone
			}
			stale = append(stale, g.Name)
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil, fmt.Errorf("rename %s: %w", path, err)
		}
	}
	// Sweep stale files (trunks deleted or now inactive) — and report their
	// gateways so they're unloaded, not just orphaned in sofia.
	entries, err := os.ReadDir(GatewayDir)
	if err != nil {
		return stale, nil // gateway dir is fresh; nothing to sweep
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "t_") || !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		if !want[e.Name()] {
			_ = os.Remove(filepath.Join(GatewayDir, e.Name()))
			if id, err := uuid.Parse(strings.TrimSuffix(strings.TrimPrefix(e.Name(), "t_"), ".xml")); err == nil {
				stale = append(stale, GatewayName(id))
			}
		}
	}
	return stale, nil
}

// RescanExternalProfile applies the gateway dir to sofia without restarting
// the profile: it unloads (killgw) each gateway in stale — changed or removed
// since the last write, per WriteGateways — then rescans, which loads new and
// changed gateways fresh. Gateways not in stale are left running, so other
// trunks' registrations and calls are untouched.
func (c *Client) RescanExternalProfile(ctx context.Context, stale []string) error {
	conn, err := eslgo.Dial(c.addr, c.password, func() {})
	if err != nil {
		slog.Error("freeswitch dial for rescan failed", "component", "freeswitch", "err", err)
		return err
	}
	defer conn.Close()
	for _, name := range stale {
		// -ERR (e.g. not loaded yet) is fine: the goal is "not loaded".
		if _, err := conn.SendCommand(ctx, command.API{
			Command:   "sofia",
			Arguments: "profile external killgw " + name,
		}); err != nil {
			slog.Error("freeswitch killgw failed", "component", "freeswitch", "gateway", name, "err", err)
			return err
		}
	}
	_, err = conn.SendCommand(ctx, command.API{
		Command:   "sofia",
		Arguments: "profile external rescan",
	})
	if err != nil {
		slog.Error("freeswitch sofia rescan failed", "component", "freeswitch", "err", err)
		return err
	}
	return nil
}

package provider

import (
	"fmt"

	"github.com/shridarpatil/tring/internal/dial"
)

// indianSIPTrunk is the shared shape for any Indian-domestic SIP trunk
// reachable via a registered FreeSWITCH gateway. The four named providers
// below (Exotel/Airtel/Jio/Tata) differ only in:
//
//   - their public `Name()` (used as the value of `numbers.provider` and
//     as the gateway name FreeSWITCH dials through), and
//   - the env-var prefix that supplies their credentials (handled in
//     freeswitch/conf/vars.xml's X-PRE-PROCESS env-set lines and the
//     matching XML in sip_profiles/external/).
//
// All four enforce Indian E.164 normalization. Switching a tenant from
// one to another is a config-only operation:
//
//  1. POST /v1/numbers with the new provider name + DID.
//  2. (Optional) Delete the old outbound number so it doesn't get picked.
//
// No code changes, no FreeSWITCH restart, no Tring restart.
type indianSIPTrunk struct {
	name string
}

func (p indianSIPTrunk) Name() string { return p.name }

func (p indianSIPTrunk) FreeSWITCHGateway() string {
	// Each provider ships its own XML at sip_profiles/external/<name>.xml.
	// This function is documentary only; FreeSWITCH reads from disk.
	return fmt.Sprintf(`<gateway name="%s">
  <param name="username" value="$${%s_sip_username}"/>
  <param name="password" value="$${%s_sip_password}"/>
  <param name="realm" value="$${%s_sip_realm}"/>
  <param name="register" value="$${%s_register}"/>
</gateway>`, p.name, p.name, p.name, p.name, p.name)
}

func (p indianSIPTrunk) DialString(dest string) string {
	return fmt.Sprintf("sofia/gateway/%s/%s", p.name, dest)
}

func (p indianSIPTrunk) NormalizeE164(raw string) (string, error) {
	return dial.India(raw)
}

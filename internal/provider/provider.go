// Package provider abstracts trunk providers (Indian carriers / raw SIP). The Tring orchestration layer talks to FreeSWITCH only — providers
// describe themselves via FreeSWITCH gateway XML and a dial-string format. A
// new provider implements this interface; no other code changes.
package provider

// Provider is the trunk-agnostic surface the orchestrator depends on.
//
// FreeSWITCHGateway returns the XML <gateway> snippet (or "" if the provider
// doesn't need one — e.g., the dev loopback). The Tring control plane is
// responsible for writing this snippet into freeswitch/conf/sip_profiles/
// external/<name>.xml and rescuing FreeSWITCH to pick it up. v1 will mount
// these dynamically; v0 ships an example file and Day 9 fills in real creds.
//
// DialString builds the B-leg the FreeSWITCH `originate` command should ring
// for a destination E.164 number. The result is dropped into the originate
// invocation verbatim, e.g.:
//
//	originate user/alice@t_dev.tring.local sofia/gateway/tata/+919876543210
//	                                       ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
//	                                       provider.DialString("+91...")
type Provider interface {
	Name() string
	FreeSWITCHGateway() string
	DialString(e164 string) string

	// NormalizeE164 cleans, normalizes, and validates a user-supplied
	// destination. Returns the canonical form to pass to DialString, or
	// an error if the number isn't routable through this provider.
	// Reject early — invalid numbers must never reach FreeSWITCH or the
	// trunk (where they cost real money to reject).
	NormalizeE164(raw string) (string, error)
}

// For returns a provider impl by name. Unknown names fall back to the
// loopback provider so callers without a configured outbound trunk still
// have a working dev path.
//
// Adding a new Indian trunk is config-only: edit `vars.xml`, drop a
// gateway XML into `freeswitch/conf/sip_profiles/external/`, add env vars
// to `.env`, and add a single case here returning newXxx(). No other
// code touches the new provider.
func For(name string) Provider {
	switch name {
	case "airtel":
		return newAirtel()
	case "jio":
		return newJio()
	case "tata":
		return newTata()
	case "loopback", "":
		return Loopback{}
	default:
		return Loopback{}
	}
}

package api

import (
	"errors"
	"regexp"
)

// Inputs that end up inside FreeSWITCH dial strings and ESL commands. Their
// syntax characters (`,` `|` `[` `]` `{` `}` `'` `=`, spaces, newlines) would
// add variables, bridge legs or whole commands, so each value is restricted to
// what it legitimately needs.

// sipUsernameRe allows plain and email-style usernames (the gateway transport
// routes on the local part; see agentleg.routingUser).
var sipUsernameRe = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}$`)

func validSipUsername(s string) bool { return sipUsernameRe.MatchString(s) }

// failoverCauseRe matches a FreeSWITCH hangup cause name or a numeric Q.850
// code — the values continue_on_fail accepts.
var failoverCauseRe = regexp.MustCompile(`^[A-Z0-9_]{1,64}$`)

const maxFailoverCauses = 32

func validFailoverCauses(causes []string) error {
	if len(causes) > maxFailoverCauses {
		return errors.New("too many failover_causes")
	}
	for _, c := range causes {
		if !failoverCauseRe.MatchString(c) {
			return errors.New("failover_causes must be FreeSWITCH cause names (e.g. NORMAL_TEMPORARY_FAILURE) or Q.850 codes")
		}
	}
	return nil
}

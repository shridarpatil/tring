package freeswitch

// registrations.go reads FreeSWITCH's core registration table so the admin
// console can show whether an agent is currently connected (registered over
// WebRTC/SIP). One ESL call per Agents-list refresh — cheap, same approach
// as gateway_status.go.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
)

// RegisteredUsers returns the set of currently-registered SIP users as
// lowercased "user@realm" keys. An agent is "online" when
// "<sip_username>@<tenant sip_domain>" is present. Returns an empty set
// (not an error) when FreeSWITCH reports zero registrations.
func (c *Client) RegisteredUsers(ctx context.Context) (map[string]bool, error) {
	conn, err := eslgo.Dial(c.addr, c.password, func() {})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := conn.SendCommand(ctx, command.API{
		Command:   "show",
		Arguments: "registrations as json",
	})
	if err != nil {
		return nil, err
	}
	return parseRegistrations(resp.Body), nil
}

// RegKey builds the lookup key used by RegisteredUsers.
func RegKey(user, realm string) string {
	return strings.ToLower(user + "@" + realm)
}

// parseRegistrations turns `show registrations as json` output into a set of
// "user@realm" keys. Fail-soft: any parse error (e.g. "-ERR" or the
// zero-rows shape that omits "rows") yields an empty set, so a transient
// FreeSWITCH hiccup shows agents offline rather than erroring the list.
func parseRegistrations(body []byte) map[string]bool {
	out := map[string]bool{}
	var doc struct {
		Rows []struct {
			RegUser string `json:"reg_user"`
			Realm   string `json:"realm"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return out
	}
	for _, r := range doc.Rows {
		if r.RegUser == "" {
			continue
		}
		out[RegKey(r.RegUser, r.Realm)] = true
	}
	return out
}

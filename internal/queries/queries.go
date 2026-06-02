// Package queries is Tring's single SQL catalog. Every statement both
// binaries run lives in queries.sql under a `-- name: <tag>` header (the
// goyesql/listmonk convention) and is bound at startup to a `query:"<tag>"`
// field on Queries. Centralizing SQL keeps it auditable in one place and
// lets query changes be diffed without wading through Go.
//
// pgx caches prepared statements per connection automatically, so referencing
// these strings by name costs nothing at runtime — the win is organizational.
package queries

import (
	"fmt"
	"reflect"
	"strings"

	tring "github.com/whatomate/tring"
)

// sqlFile is the raw catalog, embedded at the repo root (go:embed can't reach
// a parent dir, so the embed lives there — see the root tring package).
var sqlFile = tring.QueriesSQL

// Queries is the typed handle to every named statement in queries.sql. Each
// field's `query:"<tag>"` struct tag binds it to a `-- name: <tag>` block.
// Both cmd/api and cmd/orchestrator share this one struct; fields are grouped
// by the domain (and owning binary) they serve.
type Queries struct {
	// ---- api: agents ----
	CreateAgent string `query:"create-agent"`
	ListAgents  string `query:"list-agents"`
	GetAgent    string `query:"get-agent"`

	// ---- api: numbers ----
	CreateNumber             string `query:"create-number"`
	GetNumberTypeActive      string `query:"get-number-type-active"`
	CountOtherActiveOutbound string `query:"count-other-active-outbound"`
	UpdateNumber             string `query:"update-number"`
	ListNumbers              string `query:"list-numbers"`

	// ---- api: tenants ----
	UpdateTenant       string `query:"update-tenant"`
	GetTenantSipDomain string `query:"get-tenant-sip-domain"`

	// ---- api: calls ----
	GetAgentSipUsername string `query:"get-agent-sip-username"`
	CreateCallOutbound  string `query:"create-call-outbound"`
	CreateCallBridge    string `query:"create-call-bridge"`
	GetCall             string `query:"get-call"`
	ListCalls           string `query:"list-calls"`
	ListCallsByStatus   string `query:"list-calls-by-status"`

	// ---- api: mod_xml_curl directory (/fs) ----
	GetTenantIDBySipDomain string `query:"get-tenant-id-by-sip-domain"`
	GetAgentCredentials    string `query:"get-agent-credentials"`
	GetGuestSession        string `query:"get-guest-session"`

	// ---- api: console auth ----
	GetUserByEmail string `query:"get-user-by-email"`
	GetTenant      string `query:"get-tenant"`

	// ---- api: guest web-calling ----
	GetTenantByPublicKey       string `query:"get-tenant-by-public-key"`
	DeleteExpiredGuestSessions string `query:"delete-expired-guest-sessions"`
	CountActiveGuestSessions   string `query:"count-active-guest-sessions"`
	CreateGuestSession         string `query:"create-guest-session"`
	FlowUsable                 string `query:"flow-usable"`
	GetGuestConfig             string `query:"get-guest-config"`
	UpdateSupportFlow          string `query:"update-support-flow"`
	UpdateAllowedOrigins       string `query:"update-allowed-origins"`

	// ---- api: flows ----
	CreateFlow        string `query:"create-flow"`
	ListFlows         string `query:"list-flows"`
	GetFlow           string `query:"get-flow"`
	UpdateFlow        string `query:"update-flow"`
	DeleteFlow        string `query:"delete-flow"`
	GetFlowDefinition string `query:"get-flow-definition"`

	// ---- api: agent token / sdk credentials ----
	GetAgentSipCreds string `query:"get-agent-sip-creds"`

	// ---- api: webhooks ----
	CreateWebhook         string `query:"create-webhook"`
	ListWebhooks          string `query:"list-webhooks"`
	DeleteWebhook         string `query:"delete-webhook"`
	GetWebhookOwner       string `query:"get-webhook-owner"`
	ListWebhookDeliveries string `query:"list-webhook-deliveries"`

	// ---- api: trunks ----
	ListActiveTrunks     string `query:"list-active-trunks"`
	CreateTrunk          string `query:"create-trunk"`
	ListTrunks           string `query:"list-trunks"`
	GetTrunk             string `query:"get-trunk"`
	UpdateTrunk          string `query:"update-trunk"`
	DeleteTrunk          string `query:"delete-trunk"`
	SelectOutboundTrunks string `query:"select-outbound-trunks"`

	// ---- orchestrator: CDR settlement (runner.go) ----
	FailCallByJob     string `query:"fail-call-by-job"`
	MarkCallAnswered  string `query:"mark-call-answered"`
	GetCallSettlement string `query:"get-call-settlement"`
	SettleCall        string `query:"settle-call"`
	GetCallPayload    string `query:"get-call-payload"`

	// ---- orchestrator: IVR executor (ivr_executor.go) ----
	ResolveInboundFlow    string `query:"resolve-inbound-flow"`
	ExpireGuestSession    string `query:"expire-guest-session"`
	GetActiveFlowByID     string `query:"get-active-flow-by-id"`
	ListAgentSipUsernames string `query:"list-agent-sip-usernames"`
	// NOTE: agentBridgeTarget reuses GetTenantSipDomain (SELECT sip_domain ...).

	// ---- bootstrap / seed (first-boot + --seed-tenant CLI) ----
	CountTenants string `query:"count-tenants"`
	InsertTenant string `query:"insert-tenant"`
	InsertUser   string `query:"insert-user"`

	// ---- webhook notifier (shared: api + orchestrator) ----
	ListWebhookSubscribers string `query:"list-webhook-subscribers"`
	InsertWebhookDelivery  string `query:"insert-webhook-delivery"`
	UpdateDeliveryAttempt  string `query:"update-delivery-attempt"`
	MarkDeliverySuccess    string `query:"mark-delivery-success"`
	MarkDeliveryExhausted  string `query:"mark-delivery-exhausted"`
}

// MustLoad parses the embedded queries.sql and returns a populated Queries.
// It panics on a malformed file or a struct tag with no matching block —
// both are deterministic, embedded-asset invariants (like template.Must), so
// failing loudly at construction surfaces a typo on the first boot/test.
func MustLoad() *Queries {
	q, err := Load()
	if err != nil {
		panic("queries: " + err.Error())
	}
	return q
}

// Load parses the embedded queries.sql into a Queries, returning an error if
// any tagged field has no matching `-- name:` block.
func Load() (*Queries, error) {
	m := parse(sqlFile)
	q := &Queries{}
	v := reflect.ValueOf(q).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("query")
		if tag == "" {
			continue
		}
		sql, ok := m[tag]
		if !ok {
			return nil, fmt.Errorf("queries.sql: missing -- name: %s", tag)
		}
		v.Field(i).SetString(sql)
	}
	return q, nil
}

// parse splits the goyesql-style named-query format into tag→SQL: a
// `-- name: <tag>` line starts a statement that runs until the next such line
// or EOF. Plain `--` comment lines are kept inside the body (pgx ignores
// them), so per-query explanations survive the move out of Go.
func parse(b []byte) map[string]string {
	out := map[string]string{}
	var name string
	var buf []string
	flush := func() {
		if name != "" {
			out[name] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
		buf = nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "-- name:"); ok {
			flush()
			name = strings.TrimSpace(rest)
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return out
}

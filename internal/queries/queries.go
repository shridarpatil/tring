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

	tring "github.com/shridarpatil/tring"
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
	CreateAgent          string `query:"create-agent"`
	ListAgents           string `query:"list-agents"`
	GetAgent             string `query:"get-agent"`
	SetAgentAvailability string `query:"set-agent-availability"`

	// ---- api: teams (GetTeam + ListTeamAgentSips also used by orchestrator) ----
	CreateTeam        string `query:"create-team"`
	ListTeams         string `query:"list-teams"`
	ListTeamNames     string `query:"list-team-names"`
	GetTeam           string `query:"get-team"`
	UpdateTeam        string `query:"update-team"`
	DeleteTeam        string `query:"delete-team"`
	ListTeamAgents    string `query:"list-team-agents"`
	AddTeamAgent      string `query:"add-team-agent"`
	RemoveTeamAgent   string `query:"remove-team-agent"`
	ListTeamAgentSips string `query:"list-team-agent-sips"`

	// ---- api: numbers ----
	CreateNumber             string `query:"create-number"`
	GetNumberTypeActive      string `query:"get-number-type-active"`
	CountOtherActiveOutbound string `query:"count-other-active-outbound"`
	UpdateNumber             string `query:"update-number"`
	ListNumbers              string `query:"list-numbers"`

	// ---- api: tenants ----
	UpdateTenant            string `query:"update-tenant"`
	GetTenantSipDomain      string `query:"get-tenant-sip-domain"`
	GetTenantHoldMusic      string `query:"get-tenant-hold-music"`
	GetTenantTransferAudio  string `query:"get-tenant-transfer-audio"`
	GetTenantSettings       string `query:"get-tenant-settings"`
	GetTenantRecordCalls    string `query:"get-tenant-record-calls"`
	GetTenantMaxCallSeconds string `query:"get-tenant-max-call-seconds"`

	// ---- api: audio library (sound assets) ----
	CreateAudio string `query:"create-audio"`
	ListAudio   string `query:"list-audio"`
	GetAudio    string `query:"get-audio"`
	DeleteAudio string `query:"delete-audio"`

	// ---- api: dashboard live stats ----
	CountActiveAgentCalls string `query:"count-active-agent-calls"`
	StatsToday            string `query:"stats-today"`
	StatsHourly           string `query:"stats-hourly"`

	// ---- api: calls ----
	GetAgentSipUsername     string `query:"get-agent-sip-username"`
	CreateCallOutbound      string `query:"create-call-outbound"`
	CreateCallBridge        string `query:"create-call-bridge"`
	CreateCallInbound       string `query:"create-call-inbound"`
	SetCallIVRPath          string `query:"set-call-ivr-path"`
	SetCallAgent            string `query:"set-call-agent"`
	SetCallAgentID          string `query:"set-call-agent-id"`
	GetCallControlLeg       string `query:"get-call-control-leg"`
	GetAgentActiveCallLeg   string `query:"get-agent-active-call-leg"`
	SetCallCustomerLeg      string `query:"set-call-customer-leg"`
	SetCallRecording        string `query:"set-call-recording"`
	GetCallRecording        string `query:"get-call-recording"`
	GetCallForRecording     string `query:"get-call-for-recording"`
	SetCallRecordingS3      string `query:"set-call-recording-s3"`
	GetAgentBySIP           string `query:"get-agent-by-sip"`
	GetSuperviseTarget      string `query:"get-supervise-target"`
	GetUserSupervise        string `query:"get-user-supervise"`
	CreateSupervisorSession string `query:"create-supervisor-session"`
	EndSupervisorSession    string `query:"end-supervisor-session"`

	CreateAPIKey        string `query:"create-api-key"`
	ListAPIKeys         string `query:"list-api-keys"`
	RevokeAPIKey        string `query:"revoke-api-key"`
	ResolveAPIKeyTenant string `query:"resolve-api-key-tenant"`
	TouchAPIKey         string `query:"touch-api-key"`
	GetCallTenant       string `query:"get-call-tenant"`
	GetCallAgentLabel   string `query:"get-call-agent-label"`
	GetCall             string `query:"get-call"`
	ListCalls           string `query:"list-calls"`
	ListCallsByStatus   string `query:"list-calls-by-status"`
	ListActiveCalls     string `query:"list-active-calls"`
	CountCalls          string `query:"count-calls"`
	CountCallsByStatus  string `query:"count-calls-by-status"`

	// ---- api: mod_xml_curl directory (/fs) ----
	GetTenantIDBySipDomain string `query:"get-tenant-id-by-sip-domain"`
	GetAgentCredentials    string `query:"get-agent-credentials"`

	// ---- api: console auth ----
	GetUserByEmail string `query:"get-user-by-email"`
	GetTenant      string `query:"get-tenant"`

	// ---- api: guest web-calling ----
	FlowUsable        string `query:"flow-usable"`
	GetGuestConfig    string `query:"get-guest-config"`
	UpdateDefaultFlow string `query:"update-default-flow"`

	// ---- api: flows ----
	CreateFlow        string `query:"create-flow"`
	ListFlows         string `query:"list-flows"`
	ListFlowsPage     string `query:"list-flows-page"`
	CountFlows        string `query:"count-flows"`
	GetFlow           string `query:"get-flow"`
	UpdateFlow        string `query:"update-flow"`
	DeleteFlow        string `query:"delete-flow"`
	GetFlowDefinition string `query:"get-flow-definition"`

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
	ResolveInboundNumber  string `query:"resolve-inbound-number"`
	GetActiveFlowByID     string `query:"get-active-flow-by-id"`
	ListAgentSipUsernames string `query:"list-agent-sip-usernames"`
	GetTeamMOH            string `query:"get-team-moh"`
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

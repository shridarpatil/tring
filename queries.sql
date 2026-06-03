-- queries.sql — every SQL statement the API package runs, by name.
-- Loaded into the Queries struct at startup (see queries.go). Each block is
-- introduced by `-- name: <tag>`; the tag maps to a `query:"<tag>"` field.
-- Keep statements tenant-scoped (filter by tenant_id) per the schema rules.

-- ===========================================================================
-- agents
-- ===========================================================================

-- name: create-agent
INSERT INTO agents (tenant_id, sip_username, sip_password, display_name)
VALUES ($1, $2, $3, NULLIF($4, ''))
RETURNING id, tenant_id, sip_username, sip_password, COALESCE(display_name, ''), created_at;

-- name: list-agents
SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), created_at
FROM agents
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: get-agent
SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), created_at
FROM agents
WHERE id = $1 AND tenant_id = $2;

-- ===========================================================================
-- numbers
-- ===========================================================================

-- name: create-number
INSERT INTO numbers (tenant_id, e164, type, trunk_id, flow_id, provider_did_id, priority)
VALUES ($1, $2, $3::number_type, $4, $5, NULLIF($6, ''), $7)
RETURNING id, tenant_id, e164, type::text, trunk_id, flow_id, COALESCE(provider_did_id, ''), priority, active, created_at;

-- name: get-number-type-active
SELECT type::text, active FROM numbers WHERE id = $1 AND tenant_id = $2;

-- name: count-other-active-outbound
-- Guardrail for handlePatchNumber: are there other active outbound trunks
-- besides this one? Zero means we must refuse to deactivate the last trunk.
SELECT COUNT(*) FROM numbers
WHERE tenant_id = $1 AND type = 'outbound' AND active AND id != $2;

-- name: update-number
-- flow_id is tri-state: clear_flow ($3) wins, else set when $4 is non-null,
-- else keep existing.
UPDATE numbers
SET priority = COALESCE($1, priority),
    active   = COALESCE($2, active),
    flow_id  = CASE
                  WHEN $3::boolean THEN NULL
                  WHEN $4::uuid IS NOT NULL THEN $4::uuid
                  ELSE flow_id
               END
WHERE id = $5 AND tenant_id = $6
RETURNING id, tenant_id, e164, type::text, trunk_id, flow_id, COALESCE(provider_did_id, ''), priority, active, created_at;

-- name: list-numbers
SELECT id, tenant_id, e164, type::text, trunk_id, flow_id, COALESCE(provider_did_id, ''), priority, active, created_at
FROM numbers
WHERE tenant_id = $1
ORDER BY type, priority ASC, created_at DESC;

-- ===========================================================================
-- tenants
-- ===========================================================================

-- name: update-tenant
UPDATE tenants
SET outbound_strategy = COALESCE($1, outbound_strategy),
    failover_causes   = COALESCE($2::TEXT[], failover_causes)
WHERE id = $3
RETURNING id, name, sip_domain, outbound_strategy, failover_causes;

-- ===========================================================================
-- calls
-- ===========================================================================

-- name: get-agent-sip-username
SELECT sip_username FROM agents WHERE id = $1 AND tenant_id = $2;

-- name: get-tenant-sip-domain
SELECT sip_domain FROM tenants WHERE id = $1;

-- name: create-call-outbound
INSERT INTO calls (tenant_id, direction, status, from_number, to_number, agent_id, fs_uuid, fs_job_uuid)
VALUES ($1, 'outbound'::call_direction, 'ringing'::call_status, $2, $3, $4, $5, NULLIF($6,''))
RETURNING id, tenant_id, direction::text, status::text, from_number, to_number, agent_id, fs_uuid, started_at;

-- name: create-call-bridge
-- Two-PSTN-number bridge (click-to-call): no agent_id, both legs over the trunk.
INSERT INTO calls (tenant_id, direction, status, from_number, to_number, fs_uuid, fs_job_uuid)
VALUES ($1, 'outbound'::call_direction, 'ringing'::call_status, $2, $3, $4, NULLIF($5,''))
RETURNING id, tenant_id, direction::text, status::text, from_number, to_number, agent_id, fs_uuid, started_at;

-- name: create-call-inbound
-- Inbound/IVR call (incl. guest web calls). The orchestrator inserts this
-- when a call lands on the IVR socket; the global event loop then settles it
-- (answered/ended/duration/webhooks) by fs_uuid, same as outbound.
INSERT INTO calls (tenant_id, direction, status, from_number, to_number, fs_uuid, client_ref)
VALUES ($1, 'inbound'::call_direction, 'ringing'::call_status, $2, $3, $4, NULLIF($5, ''))
RETURNING id;

-- name: set-call-ivr-path
-- Persist the IVR journey (node path + DTMF + transfer outcomes) for a call,
-- matched by the FreeSWITCH channel UUID. Best-effort; the column defaults to
-- '[]' so a missed write just shows an empty path.
UPDATE calls SET ivr_path = $1::jsonb WHERE fs_uuid = $2;

-- name: set-call-agent
-- Stamp the agent who answered an inbound/IVR call (on transfer connect), so
-- the call log shows who handled it. Matched by FreeSWITCH channel UUID.
UPDATE calls SET agent_id = $1 WHERE fs_uuid = $2;

-- name: get-agent-by-sip
-- Resolve an agent id + display label from a SIP username, used to attribute
-- the answered transfer leg. Label falls back to the SIP username.
SELECT id, COALESCE(NULLIF(display_name, ''), sip_username)
FROM agents WHERE tenant_id = $1 AND sip_username = $2;

-- name: get-call-tenant
-- Owning tenant of a call by FreeSWITCH channel UUID. Used by the bridge-event
-- handler to scope the agent lookup; ErrNoRows means "not a Tring call".
SELECT tenant_id FROM calls WHERE fs_uuid = $1;

-- name: get-call-agent-label
-- Display label of the agent attributed to a call (NULL/no row if none yet).
-- The transfer step reads this back after the bridge as the source of truth
-- for "who answered", since the CHANNEL_BRIDGE event stamps agent_id reliably.
SELECT COALESCE(NULLIF(a.display_name, ''), a.sip_username)
FROM calls c JOIN agents a ON a.id = c.agent_id
WHERE c.fs_uuid = $1;

-- name: get-call
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.fs_uuid, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref
FROM calls c LEFT JOIN agents a ON a.id = c.agent_id
WHERE c.id = $1 AND c.tenant_id = $2;

-- name: list-calls
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.fs_uuid, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref
FROM calls c LEFT JOIN agents a ON a.id = c.agent_id
WHERE c.tenant_id = $1
ORDER BY c.started_at DESC LIMIT $2;

-- name: list-calls-by-status
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.fs_uuid, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref
FROM calls c LEFT JOIN agents a ON a.id = c.agent_id
WHERE c.tenant_id = $1 AND c.status = $3::call_status
ORDER BY c.started_at DESC LIMIT $2;

-- ===========================================================================
-- mod_xml_curl directory (/fs) — FreeSWITCH SIP auth lookups
-- ===========================================================================

-- name: get-tenant-id-by-sip-domain
SELECT id FROM tenants WHERE sip_domain = $1;

-- name: get-agent-credentials
SELECT id, sip_password FROM agents WHERE tenant_id = $1 AND sip_username = $2;

-- name: get-guest-session
-- Ephemeral guest SIP identity, only valid while unexpired and flow-pinned.
SELECT sip_password, flow_id, COALESCE(client_ref, '') FROM guest_sessions
WHERE tenant_id = $1 AND sip_username = $2 AND expires_at > now();

-- ===========================================================================
-- console auth
-- ===========================================================================

-- name: get-user-by-email
SELECT id, tenant_id, password_hash FROM users WHERE email = $1;

-- name: get-tenant
-- Full tenant row in the shape AuthRequired / login both load.
SELECT id, name, sip_domain, outbound_strategy, failover_causes
FROM tenants WHERE id = $1;

-- ===========================================================================
-- guest web-calling
-- ===========================================================================

-- name: get-tenant-by-public-key
SELECT id, sip_domain, support_flow_id, allowed_origins, guest_require_backend_mint
FROM tenants WHERE public_key = $1;

-- name: delete-expired-guest-sessions
DELETE FROM guest_sessions WHERE expires_at < now();

-- name: count-active-guest-sessions
SELECT count(*) FROM guest_sessions WHERE tenant_id = $1 AND expires_at > now();

-- name: create-guest-session
INSERT INTO guest_sessions (tenant_id, sip_username, sip_password, flow_id, expires_at, client_ref)
VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5), NULLIF($6, ''));

-- name: flow-usable
-- True iff the flow exists, belongs to the tenant, and is active.
SELECT true FROM flows WHERE id = $1 AND tenant_id = $2 AND is_active;

-- name: get-guest-config
SELECT COALESCE(public_key, ''), support_flow_id, allowed_origins, guest_require_backend_mint
FROM tenants WHERE id = $1;

-- name: update-support-flow
UPDATE tenants SET support_flow_id = $1 WHERE id = $2;

-- name: update-allowed-origins
UPDATE tenants SET allowed_origins = $1 WHERE id = $2;

-- name: update-guest-require-backend-mint
-- Per-tenant policy toggle: true disables the public guest mint (backend
-- API-key mint only). See handleGuestCredentials / handleAuthGuestCredentials.
UPDATE tenants SET guest_require_backend_mint = $1 WHERE id = $2;

-- ===========================================================================
-- api keys (tenant-scoped; tk_live_… bearer auth)
-- ===========================================================================

-- name: create-api-key
INSERT INTO api_keys (tenant_id, name, key_hash, key_prefix)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at;

-- name: list-api-keys
-- Never returns the hash/plaintext — only the display prefix + status.
SELECT id, name, key_prefix, created_at, last_used_at, revoked_at
FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC;

-- name: revoke-api-key
-- Soft revoke (keeps the audit row). Scoped to tenant; no-op if already revoked.
UPDATE api_keys SET revoked_at = now()
WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL;

-- name: resolve-api-key-tenant
-- Auth path: map a presented key's hash to its tenant, unless revoked.
SELECT tenant_id FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: touch-api-key
-- Best-effort last-used stamp on a successful auth.
UPDATE api_keys SET last_used_at = now() WHERE key_hash = $1;

-- ===========================================================================
-- flows
-- ===========================================================================

-- name: create-flow
INSERT INTO flows (tenant_id, name, description, is_active, is_outgoing_end, definition, welcome_audio_url)
VALUES ($1, $2, $3,
    COALESCE($4, true), COALESCE($5, false),
    COALESCE($6::jsonb, '{"version":2,"nodes":[],"edges":[],"entry_node":""}'::jsonb),
    COALESCE(NULLIF($7, ''), '')
)
RETURNING id, tenant_id, name, description, is_active, is_outgoing_end,
          definition, welcome_audio_url, created_at, updated_at;

-- name: list-flows
SELECT id, tenant_id, name, description, is_active, is_outgoing_end,
       definition, welcome_audio_url, created_at, updated_at
FROM flows WHERE tenant_id = $1 ORDER BY updated_at DESC;

-- name: get-flow
SELECT id, tenant_id, name, description, is_active, is_outgoing_end,
       definition, welcome_audio_url, created_at, updated_at
FROM flows WHERE id = $1 AND tenant_id = $2;

-- name: update-flow
UPDATE flows SET
    name             = COALESCE($1, name),
    description      = COALESCE($2, description),
    is_active        = COALESCE($3, is_active),
    is_outgoing_end  = COALESCE($4, is_outgoing_end),
    definition       = COALESCE($5::jsonb, definition),
    welcome_audio_url = COALESCE($6, welcome_audio_url),
    updated_at       = now()
WHERE id = $7 AND tenant_id = $8
RETURNING id, tenant_id, name, description, is_active, is_outgoing_end,
          definition, welcome_audio_url, created_at, updated_at;

-- name: delete-flow
DELETE FROM flows WHERE id = $1 AND tenant_id = $2;

-- name: get-flow-definition
SELECT definition::text FROM flows WHERE id = $1 AND tenant_id = $2;

-- ===========================================================================
-- agent token / sdk credentials
-- ===========================================================================

-- name: get-agent-sip-creds
SELECT a.sip_username, a.sip_password, t.sip_domain
FROM agents a JOIN tenants t ON t.id = a.tenant_id
WHERE a.id = $1 AND a.tenant_id = $2;

-- ===========================================================================
-- webhooks
-- ===========================================================================

-- name: create-webhook
INSERT INTO webhooks (tenant_id, url, secret, events)
VALUES ($1, $2, $3, $4)
RETURNING id, tenant_id, url, secret, events, created_at;

-- name: list-webhooks
SELECT id, tenant_id, url, events, created_at
FROM webhooks WHERE tenant_id = $1 ORDER BY created_at DESC;

-- name: delete-webhook
DELETE FROM webhooks WHERE id = $1 AND tenant_id = $2;

-- name: get-webhook-owner
SELECT tenant_id FROM webhooks WHERE id = $1;

-- name: list-webhook-deliveries
SELECT id, webhook_id, event_type, status::text, attempts,
       last_error, response_status, delivered_at, created_at
FROM webhook_deliveries
WHERE webhook_id = $1
ORDER BY created_at DESC LIMIT 100;

-- ===========================================================================
-- trunks
-- ===========================================================================

-- name: list-active-trunks
-- All active trunks across every tenant — drives full gateway-XML re-render.
SELECT id, tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
       register, proxy_ip, auth_acl_cidr, active, extra_params, created_at, updated_at
FROM trunks WHERE active;

-- name: create-trunk
INSERT INTO trunks (
    tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
    register, proxy_ip, auth_acl_cidr, active, extra_params
) VALUES (
    $1, $2, $3, $4, $5, $6,
    COALESCE($7, false), $8, $9, COALESCE($10, true),
    COALESCE($11::jsonb, '{}'::jsonb)
)
RETURNING id, tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
          register, proxy_ip, auth_acl_cidr, active, extra_params, created_at, updated_at;

-- name: list-trunks
SELECT id, tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
       register, proxy_ip, auth_acl_cidr, active, extra_params, created_at, updated_at
FROM trunks WHERE tenant_id = $1 ORDER BY name;

-- name: get-trunk
SELECT id, tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
       register, proxy_ip, auth_acl_cidr, active, extra_params, created_at, updated_at
FROM trunks WHERE id = $1 AND tenant_id = $2;

-- name: update-trunk
UPDATE trunks SET
    name          = COALESCE($1, name),
    provider_type = COALESCE($2, provider_type),
    sip_username  = COALESCE($3, sip_username),
    sip_password  = COALESCE($4, sip_password),
    sip_realm     = COALESCE($5, sip_realm),
    register      = COALESCE($6, register),
    proxy_ip      = COALESCE($7, proxy_ip),
    auth_acl_cidr = COALESCE($8, auth_acl_cidr),
    active        = COALESCE($9, active),
    extra_params  = COALESCE($10::jsonb, extra_params),
    updated_at    = now()
WHERE id = $11 AND tenant_id = $12
RETURNING id, tenant_id, name, provider_type, sip_username, sip_password, sip_realm,
          register, proxy_ip, auth_acl_cidr, active, extra_params, created_at, updated_at;

-- name: delete-trunk
DELETE FROM trunks WHERE id = $1 AND tenant_id = $2;

-- name: select-outbound-trunks
-- Active outbound DIDs joined to their active trunk, priority order.
SELECT n.e164, tk.id, tk.provider_type
FROM numbers n
JOIN trunks tk ON tk.id = n.trunk_id
WHERE n.tenant_id = $1
  AND n.type = 'outbound'
  AND n.active
  AND tk.active
ORDER BY n.priority ASC, n.created_at ASC;

-- ===========================================================================
-- orchestrator: CDR settlement (runner.go)
-- ===========================================================================

-- name: fail-call-by-job
-- Originate failed before any channel existed: the only signal is the bgapi
-- Job-UUID + a -ERR cause. Mark the still-ringing row failed.
UPDATE calls
SET status='failed'::call_status, ended_at=now(),
    duration_seconds=0, hangup_cause=$1
WHERE fs_job_uuid=$2 AND status='ringing'::call_status
RETURNING fs_uuid;

-- name: mark-call-answered
UPDATE calls
SET status = 'answered'::call_status, answered_at = now()
WHERE fs_uuid = $1 AND status = 'ringing'::call_status;

-- name: get-call-settlement
SELECT started_at, answered_at, status::text FROM calls WHERE fs_uuid = $1;

-- name: settle-call
UPDATE calls
SET status = $1::call_status, ended_at = now(),
    duration_seconds = $2, hangup_cause = $3
WHERE fs_uuid = $4 AND status NOT IN ('ended','failed');

-- name: get-call-payload
SELECT id, tenant_id, direction::text, status::text,
       from_number, to_number, agent_id,
       fs_uuid, COALESCE(fs_job_uuid,''),
       started_at, answered_at, ended_at,
       duration_seconds, hangup_cause, recording_url
FROM calls WHERE fs_uuid = $1;

-- ===========================================================================
-- orchestrator: IVR executor (ivr_executor.go)
-- ===========================================================================

-- name: resolve-inbound-number
-- Inbound DID → owning tenant + attached flow id (NULL if none). The flow
-- DEFINITION is loaded separately by id (get-active-flow-by-id) so it can be
-- served from the cache; this stays a cheap indexed point read that always
-- hits the DB, since a number's flow assignment can change independently of
-- the flow's contents.
SELECT n.tenant_id, n.flow_id
FROM numbers n
WHERE n.e164 = $1 AND n.type = 'inbound';

-- name: expire-guest-session
-- Single-use guest credential: drop the session once its call ends.
DELETE FROM guest_sessions WHERE sip_username = $1;

-- name: get-active-flow-by-id
-- Guest web-call path: flow pinned by UUID via tring_flow_id channel var.
SELECT tenant_id, COALESCE(definition::text, '')
FROM flows WHERE id = $1 AND is_active;

-- name: list-agent-sip-usernames
SELECT sip_username FROM agents WHERE tenant_id = $1;

-- ===========================================================================
-- bootstrap / seed (first-boot + --seed-tenant CLI)
-- ===========================================================================

-- name: count-tenants
SELECT COUNT(*) FROM tenants;

-- name: insert-tenant
-- Shared by bootstrap (env sentinels) and the --seed-tenant CLI (real key).
INSERT INTO tenants (name, api_key_hash, api_key_prefix, sip_domain)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: insert-user
INSERT INTO users (tenant_id, email, password_hash)
VALUES ($1, $2, $3);

-- ===========================================================================
-- webhook notifier (shared: api + orchestrator)
-- ===========================================================================

-- name: list-webhook-subscribers
-- Hooks for the tenant subscribed to this event (empty events[] = all).
SELECT id, url, secret, events
FROM webhooks
WHERE tenant_id = $1
  AND (cardinality(events) = 0 OR $2 = ANY(events));

-- name: insert-webhook-delivery
INSERT INTO webhook_deliveries (webhook_id, event_type, payload)
VALUES ($1, $2, $3) RETURNING id;

-- name: update-delivery-attempt
UPDATE webhook_deliveries
SET attempts = $1, response_status = NULLIF($2, 0), last_error = NULLIF($3, '')
WHERE id = $4;

-- name: mark-delivery-success
UPDATE webhook_deliveries
SET status='success', delivered_at=now(), response_status=$1, last_error=NULL
WHERE id=$2;

-- name: mark-delivery-exhausted
UPDATE webhook_deliveries
SET status='exhausted', response_status=NULLIF($1,0), last_error=NULLIF($2,'')
WHERE id=$3;

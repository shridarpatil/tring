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
RETURNING id, tenant_id, sip_username, sip_password, COALESCE(display_name, ''), available_for_calls, created_at;

-- name: list-agents
SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), available_for_calls, created_at
FROM agents
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: get-agent
SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), available_for_calls, created_at
FROM agents
WHERE id = $1 AND tenant_id = $2;

-- name: set-agent-availability
UPDATE agents SET available_for_calls = $1
WHERE id = $2 AND tenant_id = $3
RETURNING id, tenant_id, sip_username, COALESCE(display_name, ''), available_for_calls, created_at;

-- ===========================================================================
-- teams (tenant-scoped agent groups; identified by name)
-- ===========================================================================

-- name: create-team
INSERT INTO teams (tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, created_at;

-- name: list-teams
SELECT tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, created_at
FROM teams WHERE tenant_id = $1 ORDER BY name;

-- name: list-team-names
-- Just the names — for the dashboard queue-depth lookup (one Redis read per team).
SELECT name FROM teams WHERE tenant_id = $1 ORDER BY name;

-- name: get-team
SELECT tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, created_at
FROM teams WHERE tenant_id = $1 AND name = $2;

-- name: get-team-moh
-- Team queue settings + the tenant's system hold music, for the orchestrator's
-- effective-MOH resolution (team override → tenant hold music → built-in default).
SELECT t.strategy, t.ring_seconds, t.max_wait_seconds, t.moh_sound,
       COALESCE((SELECT hold_music FROM tenants WHERE id = t.tenant_id), '')
FROM teams t WHERE t.tenant_id = $1 AND t.name = $2;

-- name: update-team
-- Partial update: a NULL arg keeps the existing value (name is the PK, immutable).
UPDATE teams SET
    strategy         = COALESCE($3, strategy),
    ring_seconds     = COALESCE($4, ring_seconds),
    max_wait_seconds = COALESCE($5, max_wait_seconds),
    moh_sound        = COALESCE($6, moh_sound)
WHERE tenant_id = $1 AND name = $2
RETURNING tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, created_at;

-- name: delete-team
DELETE FROM teams WHERE tenant_id = $1 AND name = $2;

-- name: list-team-agents
-- Members of a team with their membership position, for the console editor.
SELECT a.id, a.sip_username, COALESCE(a.display_name, ''), a.available_for_calls, ta.position
FROM team_agents ta JOIN agents a ON a.id = ta.agent_id
WHERE ta.tenant_id = $1 AND ta.team_name = $2
ORDER BY ta.position, a.sip_username;

-- name: add-team-agent
-- Idempotent: re-adding updates position. The SELECT…WHERE EXISTS guards that
-- the agent belongs to this tenant (cross-tenant add inserts nothing → 0 rows).
INSERT INTO team_agents (tenant_id, team_name, agent_id, position)
SELECT $1, $2, $3, $4
WHERE EXISTS (SELECT 1 FROM agents WHERE id = $3 AND tenant_id = $1)
ON CONFLICT (tenant_id, team_name, agent_id) DO UPDATE SET position = EXCLUDED.position;

-- name: remove-team-agent
DELETE FROM team_agents WHERE tenant_id = $1 AND team_name = $2 AND agent_id = $3;

-- name: list-team-agent-sips
-- Ring targets for a team: members the backend hasn't marked unavailable,
-- ordered by membership position (used directly by the `sequential` strategy).
SELECT a.sip_username, ta.position
FROM team_agents ta JOIN agents a ON a.id = ta.agent_id
WHERE ta.tenant_id = $1 AND ta.team_name = $2 AND a.available_for_calls
ORDER BY ta.position, a.sip_username;

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
SET outbound_strategy       = COALESCE($1, outbound_strategy),
    failover_causes         = COALESCE($2::TEXT[], failover_causes),
    hold_music              = COALESCE($4, hold_music),
    record_calls            = COALESCE($5, record_calls),
    max_call_seconds        = COALESCE($6, max_call_seconds),
    transfer_failed_message = COALESCE($7, transfer_failed_message),
    transfer_failed_audio   = COALESCE($8, transfer_failed_audio),
    recording_sample_rate   = COALESCE($9, recording_sample_rate),
    recording_format        = COALESCE($10, recording_format)
WHERE id = $3
RETURNING id, name, sip_domain, outbound_strategy, failover_causes, hold_music, record_calls, max_call_seconds, transfer_failed_message, recording_sample_rate, recording_format;

-- name: get-tenant-settings
-- Tenant-level settings the Settings page reads (not carried in the auth context).
SELECT hold_music, record_calls, max_call_seconds, transfer_failed_message, recording_sample_rate, recording_format FROM tenants WHERE id = $1;

-- name: get-tenant-max-call-seconds
-- Tenant's max call duration — the ephemeral TURN credential lifetime basis.
SELECT max_call_seconds FROM tenants WHERE id = $1;

-- name: get-tenant-record-calls
-- Whether the orchestrator should record a connected call for this tenant, and
-- the sample rate (Hz) and format the finished recording is encoded in.
SELECT record_calls, recording_sample_rate, recording_format FROM tenants WHERE id = $1;

-- name: set-call-recording
-- Mark a call as recording: point recording_url at the authenticated serve
-- endpoint (keyed by the call's own id). Idempotent — only sets it once.
UPDATE calls SET recording_url = '/v1/calls/' || id::text || '/recording'
WHERE leg_id = $1 AND recording_url IS NULL;

-- name: get-call-recording
-- Resolve a call's recording location for the serve endpoint, scoped to the
-- tenant so one tenant can't fetch another's. recording_s3_key NULL → serve the
-- local spool by leg_id; non-NULL → stream from S3.
SELECT leg_id, recording_s3_key FROM calls WHERE id = $1 AND tenant_id = $2;

-- name: get-call-for-recording
-- Tenant + start time for an in-flight recording, by call leg id — used by
-- the orchestrator on RECORD_STOP to build the date-wise S3 key.
SELECT tenant_id, started_at FROM calls WHERE leg_id = $1;

-- name: set-call-recording-s3
-- Mark a recording as stored in S3 (after a successful upload).
UPDATE calls SET recording_s3_key = $2 WHERE leg_id = $1;

-- name: get-tenant-hold-music
SELECT hold_music FROM tenants WHERE id = $1;

-- name: get-tenant-transfer-audio
-- The synthesized "no one answered" prompt path (played on a failed blind transfer),
-- "" when unset (TTS disabled at save). Read by the gateway on the pion plane.
SELECT COALESCE(transfer_failed_audio, '') FROM tenants WHERE id = $1;

-- ===========================================================================
-- audio assets (tenant sound library)
-- ===========================================================================

-- name: create-audio
INSERT INTO audio_assets (tenant_id, name, file_path, format, size_bytes)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, tenant_id, name, file_path, format, size_bytes, created_at;

-- name: list-audio
SELECT id, tenant_id, name, file_path, format, size_bytes, created_at
FROM audio_assets WHERE tenant_id = $1 ORDER BY created_at DESC;

-- name: get-audio
SELECT id, tenant_id, name, file_path, format, size_bytes, created_at
FROM audio_assets WHERE id = $1 AND tenant_id = $2;

-- name: delete-audio
DELETE FROM audio_assets WHERE id = $1 AND tenant_id = $2;

-- name: count-active-agent-calls
-- Live calls currently connected to an agent (answered + an agent stamped, not
-- yet ended) — powers the dashboard "on call" stat.
SELECT COUNT(*) FROM calls
WHERE tenant_id = $1 AND status = 'answered' AND agent_id IS NOT NULL;

-- name: stats-today
-- Running "today so far" totals for the cockpit: calls started today, answered,
-- abandoned, failed, plus average wait (time-to-answer) and talk time. Day
-- boundary is server-local midnight (date_trunc on now()). avgs are NULL with no
-- qualifying calls — the handler coalesces to 0.
--   abandoned = the call was transferred to a team/agent (an ivr_path "transfer"
--     step exists) but ended without any agent connecting (agent_id IS NULL —
--     stamped ONLY on answer, on both the phone set-call-agent and web/pion
--     set-call-agent-id paths). Both planes persist the transfer step even when
--     the caller bails (the web plane stamps the journey on IVR teardown before the
--     CDR settles). There is no 'abandoned' call_status, so this IS the signal.
--   failed = status 'failed' (originate/connect error before the call got going).
--   wait = time-to-answer (started_at→answered_at); not pure queue time (we don't
--     persist a per-caller enqueue timestamp).
--   talk = answer→end, over calls someone answered. IVR-only and unanswered
--     calls have no talk time. Outbound rows only exist once the callee picks
--     up, so they count from started_at.
SELECT
  count(*),
  count(*) FILTER (WHERE answered_at IS NOT NULL),
  count(*) FILTER (WHERE agent_id IS NULL AND ended_at IS NOT NULL
                     AND ivr_path @> '[{"node_type":"transfer"}]'),
  count(*) FILTER (WHERE status = 'failed'),
  COALESCE(avg(EXTRACT(EPOCH FROM (answered_at - started_at))) FILTER (WHERE answered_at IS NOT NULL), 0)::float8,
  COALESCE(avg(EXTRACT(EPOCH FROM (ended_at - COALESCE(answered_at, started_at))))
             FILTER (WHERE ended_at IS NOT NULL AND (answered_at IS NOT NULL OR direction = 'outbound')), 0)::float8
FROM calls
WHERE tenant_id = $1 AND started_at >= date_trunc('day', now());

-- name: stats-hourly
-- Calls per hour for the last 12h for the trend chart, ALWAYS 12 rows (gaps
-- filled with 0 via generate_series). Bucketing and gap-filling both live here so
-- the series and the data share ONE date_trunc in the DB's session timezone (set
-- by the deploy, e.g. Asia/Kolkata) — the handler does no timezone math, so there's
-- nothing to mismatch. (The previous handler truncated buckets in UTC and matched
-- by unix epoch, which zeroed the whole chart on any fractional-offset session zone
-- like IST +5:30, where local hour starts fall at :30 past the UTC hour.)
SELECT g.hour, COALESCE(c.n, 0)
FROM generate_series(
       date_trunc('hour', now()) - interval '11 hours',
       date_trunc('hour', now()),
       interval '1 hour'
     ) AS g(hour)
LEFT JOIN (
  SELECT date_trunc('hour', started_at) AS hour, count(*) AS n
  FROM calls
  WHERE tenant_id = $1 AND started_at >= date_trunc('hour', now()) - interval '11 hours'
  GROUP BY 1
) c ON c.hour = g.hour
ORDER BY g.hour;

-- ===========================================================================
-- calls
-- ===========================================================================

-- name: get-agent-sip-username
SELECT sip_username FROM agents WHERE id = $1 AND tenant_id = $2;

-- name: get-tenant-sip-domain
SELECT sip_domain FROM tenants WHERE id = $1;

-- name: create-call-outbound
-- $7 is the pion room id (media plane) so a supervisor can be routed to the
-- outbound call's live room; NULL for FreeSWITCH-path outbound calls, which
-- have no pion room.
INSERT INTO calls (tenant_id, direction, status, from_number, to_number, agent_id, leg_id, job_uuid, source, room_id)
VALUES ($1, 'outbound'::call_direction, 'ringing'::call_status, $2, $3, $4, $5, NULLIF($6,''), 'phone'::call_source, NULLIF($7, ''))
RETURNING id, tenant_id, direction::text, status::text, from_number, to_number, agent_id, leg_id, started_at, source::text;

-- name: create-call-bridge
-- Two-PSTN-number bridge (click-to-call): no agent_id, both legs over the trunk.
INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, job_uuid, source)
VALUES ($1, 'outbound'::call_direction, 'ringing'::call_status, $2, $3, $4, NULLIF($5,''), 'phone'::call_source)
RETURNING id, tenant_id, direction::text, status::text, from_number, to_number, agent_id, leg_id, started_at, source::text;

-- name: create-call-inbound
-- Inbound/IVR call (incl. guest web calls). $6 is the channel source
-- ('web' | 'phone'); $7 is the pion room id (media plane) so a supervisor can be
-- routed to the live room. The global event loop settles it by leg_id.
INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, client_ref, source, room_id)
VALUES ($1, 'inbound'::call_direction, 'ringing'::call_status, $2, $3, $4, NULLIF($5, ''), $6::call_source, NULLIF($7, ''))
RETURNING id;

-- name: get-supervise-target
-- Resolve a call's pion room + status for a supervisor join. Scoped to the
-- tenant. room_id is NULL for non-media-plane calls (nothing to supervise).
SELECT COALESCE(room_id, ''), status::text
FROM calls WHERE id = $1 AND tenant_id = $2;

-- name: get-user-supervise
-- The supervise permission for a console user, scoped to the tenant.
SELECT can_supervise FROM users WHERE id = $1 AND tenant_id = $2;

-- name: create-supervisor-session
INSERT INTO supervisor_sessions (tenant_id, call_id, supervisor_user_id, started_at, modes_used)
VALUES ($1, $2, $3, now(), $4)
RETURNING id;

-- name: end-supervisor-session
UPDATE supervisor_sessions SET ended_at = now(), modes_used = $2 WHERE id = $1;

-- name: set-call-ivr-path
-- Persist the IVR journey (node path + DTMF + transfer outcomes) for a call,
-- matched by the call leg id. Best-effort; the column defaults to
-- '[]' so a missed write just shows an empty path.
UPDATE calls SET ivr_path = $1::jsonb WHERE leg_id = $2;

-- name: set-call-agent
-- Stamp the agent who answered an inbound/IVR call (on transfer connect), so
-- the call log shows who handled it, plus the agent's FreeSWITCH leg ($3) for
-- call control (hold/transfer). Matched by the caller leg id.
UPDATE calls SET agent_id = $1, agent_leg_id = $3 WHERE leg_id = $2;

-- name: set-call-agent-id
-- Stamp just the answering agent on a media-plane call (callcdr) — no FS leg, the
-- agent is on the pion plane. Matched by the call's leg id.
UPDATE calls SET agent_id = $2 WHERE leg_id = $1;

-- name: insert-call-event
-- Append one event to a call's timeline (internal/callcdr). Best-effort; a missed
-- write just leaves a gap in the history. detail is type-specific JSON.
INSERT INTO call_events (tenant_id, call_id, type, detail)
VALUES ($1, $2, $3, $4::jsonb);

-- name: list-call-events
-- The full ordered timeline for one call, tenant-scoped (the console call detail).
SELECT type, at, detail
FROM call_events
WHERE call_id = $1 AND tenant_id = $2
ORDER BY at ASC, seq ASC;

-- name: get-call-control-leg
-- The CUSTOMER leg (the party that should hear hold music), the call's status,
-- and the tenant's hold music — scoped to the tenant. Hold injects MOH into this
-- leg with uuid_displace (NOT uuid_hold, which would re-INVITE a media-gateway
-- leg and drop the call). COALESCE: inbound leaves customer_leg_id NULL because
-- leg_id already is the visitor leg; outbound stores the trunk B-leg.
SELECT COALESCE(c.customer_leg_id, c.leg_id), c.status, t.hold_music
FROM calls c JOIN tenants t ON t.id = c.tenant_id
WHERE c.id = $1 AND c.tenant_id = $2;

-- name: get-agent-active-call-leg
-- The agent's currently-connected call, for SDK self-control (hold). Resolved
-- from the agent JWT (tenant + agent id), so the agent can only control their
-- OWN call. Returns the CUSTOMER leg (hold-music target) + the tenant's hold
-- music; newest answered call wins if somehow more than one.
SELECT COALESCE(c.customer_leg_id, c.leg_id), t.hold_music
FROM calls c JOIN tenants t ON t.id = c.tenant_id
WHERE c.tenant_id = $1 AND c.agent_id = $2 AND c.status = 'answered'
ORDER BY c.answered_at DESC NULLS LAST LIMIT 1;

-- name: set-call-customer-leg
-- Record the customer/visitor leg (the outbound bridged B-leg) so hold plays MOH
-- to the right party. Matched by the tracked leg id.
UPDATE calls SET customer_leg_id = $1 WHERE leg_id = $2;

-- name: get-agent-by-sip
-- Resolve an agent id + display label from a SIP username, used to attribute
-- the answered transfer leg. Label falls back to the SIP username.
SELECT id, COALESCE(NULLIF(display_name, ''), sip_username)
FROM agents WHERE tenant_id = $1 AND sip_username = $2;

-- name: get-call-tenant
-- Owning tenant of a call by leg id. Used by the bridge-event
-- handler to scope the agent lookup; ErrNoRows means "not a Tring call".
SELECT tenant_id FROM calls WHERE leg_id = $1;

-- name: get-call-agent-label
-- Display label of the agent attributed to a call (NULL/no row if none yet).
-- The transfer step reads this back after the bridge as the source of truth
-- for "who answered", since the CHANNEL_BRIDGE event stamps agent_id reliably.
SELECT COALESCE(NULLIF(a.display_name, ''), a.sip_username)
FROM calls c JOIN agents a ON a.id = c.agent_id
WHERE c.leg_id = $1;

-- name: get-call
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, '')
FROM calls c LEFT JOIN agents a ON a.id = c.agent_id
WHERE c.id = $1 AND c.tenant_id = $2;

-- name: list-calls
-- One query for every filter combination (no string building): '' disables a
-- filter. $4 status (compared as text, so an unknown value matches nothing
-- instead of failing the enum cast); $5 call-id prefix (calls.id or leg_id);
-- $6 phone digits, matched inside from/to with formatting stripped. A search
-- sets $5, $6 or both (an all-digit id prefix); a call matching either is found.
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, '')
FROM calls c LEFT JOIN agents a ON a.id = c.agent_id
WHERE c.tenant_id = $1
  AND ($4 = '' OR c.status::text = $4)
  AND (($5 = '' AND $6 = '')
       OR ($5 <> '' AND (c.id::text LIKE $5 || '%' OR c.leg_id LIKE $5 || '%'))
       OR ($6 <> '' AND (regexp_replace(c.from_number, '[^0-9]', '', 'g') LIKE '%' || $6 || '%'
                      OR regexp_replace(c.to_number,   '[^0-9]', '', 'g') LIKE '%' || $6 || '%')))
ORDER BY c.started_at DESC LIMIT $2 OFFSET $3;

-- name: list-active-calls
-- In-progress calls for the cockpit "Live calls" panel: not yet ended, still
-- ringing or connected. Oldest first so the longest-running call stays put at the
-- top. Capped — active calls are naturally bounded, the LIMIT is just a backstop.
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, '')
FROM calls c LEFT JOIN agents a ON a.id = c.agent_id
WHERE c.tenant_id = $1 AND c.ended_at IS NULL AND c.status IN ('ringing','answered')
ORDER BY c.started_at ASC LIMIT 100;

-- name: count-calls
-- Same filters as list-calls: $2 status, $3 call-id prefix, $4 phone digits.
SELECT count(*) FROM calls c
WHERE c.tenant_id = $1
  AND ($2 = '' OR c.status::text = $2)
  AND (($3 = '' AND $4 = '')
       OR ($3 <> '' AND (c.id::text LIKE $3 || '%' OR c.leg_id LIKE $3 || '%'))
       OR ($4 <> '' AND (regexp_replace(c.from_number, '[^0-9]', '', 'g') LIKE '%' || $4 || '%'
                      OR regexp_replace(c.to_number,   '[^0-9]', '', 'g') LIKE '%' || $4 || '%')));

-- ===========================================================================
-- mod_xml_curl directory (/fs) — FreeSWITCH SIP auth lookups
-- ===========================================================================

-- name: get-tenant-id-by-sip-domain
SELECT id FROM tenants WHERE sip_domain = $1;

-- name: get-agent-credentials
SELECT id, sip_password FROM agents WHERE tenant_id = $1 AND sip_username = $2;

-- ===========================================================================
-- console auth
-- ===========================================================================

-- name: get-user-by-email
SELECT id, tenant_id, password_hash FROM users WHERE email = $1;

-- name: user-exists
-- Console-session revocation check: a JWT for a deleted user (or one moved
-- out of the tenant) must stop working. Primary-key point read.
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND tenant_id = $2);

-- name: get-tenant
-- Full tenant row in the shape AuthRequired / login both load.
SELECT id, name, sip_domain, outbound_strategy, failover_causes
FROM tenants WHERE id = $1;

-- ===========================================================================
-- guest web-calling
-- ===========================================================================

-- name: flow-usable
-- True iff the flow exists, belongs to the tenant, and is active.
SELECT true FROM flows WHERE id = $1 AND tenant_id = $2 AND is_active;

-- name: get-guest-config
SELECT default_flow_id FROM tenants WHERE id = $1;

-- name: update-default-flow
UPDATE tenants SET default_flow_id = $1 WHERE id = $2;

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

-- name: list-flows-page
SELECT id, tenant_id, name, description, is_active, is_outgoing_end,
       definition, welcome_audio_url, created_at, updated_at
FROM flows WHERE tenant_id = $1 ORDER BY updated_at DESC LIMIT $2 OFFSET $3;

-- name: count-flows
SELECT count(*) FROM flows WHERE tenant_id = $1;

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
    -- write-only secret: omitted OR empty keeps the stored password, so the
    -- console can save other fields without re-entering it.
    sip_password  = COALESCE(NULLIF($4, ''), sip_password),
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
JOIN trunks tk ON tk.id = n.trunk_id AND tk.tenant_id = n.tenant_id
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
WHERE job_uuid=$2 AND status='ringing'::call_status
RETURNING leg_id;

-- name: mark-call-answered
UPDATE calls
SET status = 'answered'::call_status, answered_at = now()
WHERE leg_id = $1 AND status = 'ringing'::call_status;

-- name: get-call-settlement
SELECT started_at, answered_at, status::text FROM calls WHERE leg_id = $1;

-- name: settle-call
UPDATE calls
SET status = $1::call_status, ended_at = now(),
    duration_seconds = $2, hangup_cause = $3
WHERE leg_id = $4 AND status NOT IN ('ended','failed');

-- name: get-call-payload
SELECT id, tenant_id, direction::text, status::text,
       from_number, to_number, agent_id,
       leg_id, COALESCE(job_uuid,''),
       started_at, answered_at, ended_at,
       duration_seconds, hangup_cause, recording_url, source::text
FROM calls WHERE leg_id = $1;

-- ===========================================================================
-- orchestrator: IVR executor (ivr_executor.go)
-- ===========================================================================

-- name: tenant-owns-number
-- Is e164 one of the tenant's active numbers (any type)? Gates an explicit
-- caller-ID override (masked calling) so a tenant can only present numbers it
-- holds — an inbound mask number (e.g. one an agent calls back) counts too.
SELECT EXISTS (
    SELECT 1 FROM numbers WHERE tenant_id = $1 AND e164 = $2 AND active
);

-- name: tenant-owns-trunk
SELECT EXISTS (SELECT 1 FROM trunks WHERE id = $1 AND tenant_id = $2);

-- name: tenant-owns-flow
SELECT EXISTS (SELECT 1 FROM flows WHERE id = $1 AND tenant_id = $2);

-- name: tenant-owns-audio-path
-- Is file_path one of the tenant's uploaded sounds? Gates hold_music, which is
-- handed to FreeSWITCH (uuid_displace / transfer_ringback) as-is.
SELECT EXISTS (SELECT 1 FROM audio_assets WHERE tenant_id = $1 AND file_path = $2);

-- name: inbound-number-held-elsewhere
-- Is e164 already an inbound number of ANOTHER tenant? Inbound routing is by
-- DID alone, so a second tenant registering it could take the first's calls.
SELECT EXISTS (
    SELECT 1 FROM numbers WHERE e164 = $1 AND type = 'inbound' AND tenant_id <> $2
);

-- name: resolve-inbound-number
-- Inbound DID → owning tenant + attached flow id (NULL if none). The flow
-- DEFINITION is loaded separately by id (get-active-flow-by-id) so it can be
-- served from the cache; this stays a cheap indexed point read that always
-- hits the DB, since a number's flow assignment can change independently of
-- the flow's contents.
--
-- Cross-tenant guards: the flow must belong to the number's own tenant (a
-- foreign flow_id resolves to NULL → call rejected), and if two tenants ever
-- hold the same inbound DID the first registrant wins (create-number refuses
-- new duplicates; this keeps routing deterministic for any that predate it).
-- A deactivated number (active = false) no longer routes.
SELECT n.tenant_id, f.id
FROM numbers n
LEFT JOIN flows f ON f.id = n.flow_id AND f.tenant_id = n.tenant_id
WHERE n.e164 = $1 AND n.type = 'inbound' AND n.active
ORDER BY n.created_at ASC, n.id ASC
LIMIT 1;

-- name: get-active-flow-by-id
-- Guest web-call path: flow pinned by UUID via tring_flow_id channel var.
-- name is used as the call log's "To" for web calls (no dialed number).
-- Scoped to the caller's tenant ($2): a flow id from another tenant (an agent
-- can put any flow_id in a join) must not resolve.
SELECT tenant_id, name, COALESCE(definition::text, '')
FROM flows WHERE id = $1 AND tenant_id = $2 AND is_active;

-- name: list-agent-sip-usernames
-- Ring targets only: skip agents the backend marked unavailable (Away/Break).
-- Connection presence (online/on_call) is checked separately by the caller.
SELECT sip_username FROM agents WHERE tenant_id = $1 AND available_for_calls;

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
INSERT INTO users (tenant_id, email, password_hash, can_supervise)
VALUES ($1, $2, $3, $4);

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

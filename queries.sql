-- queries.sql — every SQL statement the API package runs, by name.
-- Loaded into the Queries struct at startup (see queries.go). Each block is
-- introduced by `-- name: <tag>`; the tag maps to a `query:"<tag>"` field.
-- Keep statements tenant-scoped (filter by tenant_id) per the schema rules.

-- ===========================================================================
-- agents
-- ===========================================================================

-- name: create-agent
INSERT INTO users (tenant_id, sip_username, sip_password, display_name)
VALUES ($1, $2, $3, NULLIF($4, ''))
RETURNING id, tenant_id, sip_username, sip_password, COALESCE(display_name, ''), available_for_calls, created_at, role;

-- name: list-agents
SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), available_for_calls, created_at, role
FROM users
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: get-agent
SELECT id, tenant_id, sip_username, COALESCE(display_name, ''), available_for_calls, created_at, role
FROM users
WHERE id = $1 AND tenant_id = $2;

-- name: set-agent-availability
UPDATE users SET available_for_calls = $1
WHERE id = $2 AND tenant_id = $3
RETURNING id, tenant_id, sip_username, COALESCE(display_name, ''), available_for_calls, created_at, role;

-- name: get-agent-availability
-- The gateway reads this when an agent's presence socket connects, so the SDK
-- shows Away for an agent the backend already marked unavailable.
SELECT available_for_calls FROM users WHERE tenant_id = $1 AND sip_username = $2;

-- name: get-agent-mask-role
-- An agent's role and whether they manage any team, by SIP username: what the
-- gateway needs to decide whether their ring shows a masked caller number.
SELECT u.role,
       EXISTS (SELECT 1 FROM team_agents ta
               WHERE ta.tenant_id = u.tenant_id AND ta.agent_id = u.id AND ta.is_manager)
FROM users u WHERE u.tenant_id = $1 AND u.sip_username = $2;

-- ===========================================================================
-- teams (tenant-scoped agent groups; identified by name)
-- ===========================================================================

-- name: create-team
INSERT INTO teams (tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, min_live_agents, sla_seconds, hours)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, min_live_agents, sla_seconds, hours, created_at;

-- name: list-teams
SELECT tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, min_live_agents, sla_seconds, hours, created_at
FROM teams WHERE tenant_id = $1 ORDER BY name;

-- name: list-team-names
-- Just the names — for the dashboard queue-depth lookup (one Redis read per team).
SELECT name FROM teams WHERE tenant_id = $1 ORDER BY name;

-- name: get-team
SELECT tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, min_live_agents, sla_seconds, hours, created_at
FROM teams WHERE tenant_id = $1 AND name = $2;

-- name: get-team-moh
-- A team's own queue settings for the gateway's transfer ring. The tenant's
-- hold music (the effective-MOH fallback) is read and cached separately
-- (get-tenant-audio), so each invalidates on its own write.
SELECT strategy, ring_seconds, max_wait_seconds, moh_sound, sla_seconds
FROM teams WHERE tenant_id = $1 AND name = $2;

-- name: update-team
-- Partial update: a NULL arg keeps the existing value (name is the PK, immutable).
UPDATE teams SET
    strategy         = COALESCE($3, strategy),
    ring_seconds     = COALESCE($4, ring_seconds),
    max_wait_seconds = COALESCE($5, max_wait_seconds),
    moh_sound        = COALESCE($6, moh_sound),
    min_live_agents  = COALESCE($7, min_live_agents),
    sla_seconds      = COALESCE($8, sla_seconds),
    hours            = COALESCE($9, hours)
WHERE tenant_id = $1 AND name = $2
RETURNING tenant_id, name, strategy, ring_seconds, max_wait_seconds, moh_sound, min_live_agents, sla_seconds, hours, created_at;

-- name: delete-team
DELETE FROM teams WHERE tenant_id = $1 AND name = $2;

-- name: list-team-agents
-- Members of a team with their membership position, for the console editor.
SELECT a.id, a.sip_username, COALESCE(a.display_name, ''), a.available_for_calls, ta.position
FROM team_agents ta JOIN users a ON a.id = ta.agent_id
WHERE ta.tenant_id = $1 AND ta.team_name = $2
ORDER BY ta.position, a.sip_username;

-- name: add-team-agent
-- Idempotent: re-adding updates position. The SELECT…WHERE EXISTS guards that
-- the agent belongs to this tenant (cross-tenant add inserts nothing → 0 rows).
INSERT INTO team_agents (tenant_id, team_name, agent_id, position)
SELECT $1, $2, $3, $4
WHERE EXISTS (SELECT 1 FROM users WHERE id = $3 AND tenant_id = $1)
ON CONFLICT (tenant_id, team_name, agent_id) DO UPDATE SET position = EXCLUDED.position;

-- name: remove-team-agent
DELETE FROM team_agents WHERE tenant_id = $1 AND team_name = $2 AND agent_id = $3;

-- name: list-team-agent-sips
-- Ring targets for a team: members the backend hasn't marked unavailable,
-- ordered by membership position (used directly by the `sequential` strategy).
SELECT a.sip_username, ta.position
FROM team_agents ta JOIN users a ON a.id = ta.agent_id
WHERE ta.tenant_id = $1 AND ta.team_name = $2 AND a.available_for_calls
ORDER BY ta.position, a.sip_username;

-- name: list-team-live-config
-- Staffing / SLA / working-hours settings per team, for the live dashboard.
SELECT name, min_live_agents, sla_seconds, hours
FROM teams WHERE tenant_id = $1 ORDER BY name;

-- name: list-team-agent-status
-- Team membership (team name + agent SIP user) for the live dashboard's
-- per-team live-agent count; presence is joined from Redis by the caller.
SELECT ta.team_name, a.sip_username
FROM team_agents ta JOIN users a ON a.id = ta.agent_id
WHERE ta.tenant_id = $1;

-- name: insert-queue-stint
-- A caller starts waiting in a team queue. sla_seconds is the team's SLA
-- snapshotted now so later setting changes don't rewrite history.
INSERT INTO queue_stints (tenant_id, call_id, team, sla_seconds)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: end-queue-stint
-- Close a stint with its outcome (answered | abandoned | timeout | transferred).
-- Idempotent: an already-ended stint is left untouched.
UPDATE queue_stints SET ended_at = now(), outcome = $2
WHERE id = $1 AND ended_at IS NULL;

-- name: list-open-stints
-- Callers currently waiting. The 4 h cap ignores stints orphaned by a crash
-- that never got an end-queue-stint.
SELECT team, enqueued_at
FROM queue_stints
WHERE tenant_id = $1 AND ended_at IS NULL AND enqueued_at > now() - interval '4 hours'
ORDER BY enqueued_at;

-- name: count-breaches-today-by-team
-- SLA breaches since local midnight, open stints included. A breach is
-- waiting longer than the snapshotted SLA, whatever the outcome.
SELECT team, count(*)
FROM queue_stints
WHERE tenant_id = $1
  AND enqueued_at >= date_trunc('day', now())
  AND sla_seconds > 0
  AND COALESCE(ended_at, now()) - enqueued_at > make_interval(secs => sla_seconds)
GROUP BY team;

-- ===========================================================================
-- numbers
-- ===========================================================================

-- name: create-number
INSERT INTO numbers (tenant_id, e164, type, trunk_id, flow_id, provider_did_id, priority)
VALUES ($1, $2, $3::number_type, $4, $5, NULLIF($6, ''), $7)
RETURNING id, tenant_id, e164, type::text, trunk_id, flow_id, COALESCE(provider_did_id, ''), priority, active, created_at;

-- name: get-number-for-update
-- Current state handlePatchNumber validates an edit against.
SELECT e164, type::text, active, trunk_id FROM numbers WHERE id = $1 AND tenant_id = $2;

-- name: count-other-active-outbound
-- Guardrail for handlePatchNumber: are there other active outbound trunks
-- besides this one? Zero means we must refuse to deactivate the last trunk.
SELECT COUNT(*) FROM numbers
WHERE tenant_id = $1 AND type = 'outbound' AND active AND id != $2;

-- name: update-number
-- flow_id is tri-state: clear_flow ($3) wins, else set when $4 is non-null,
-- else keep existing. A NULL e164/type/trunk_id ($7-$9) keeps the existing.
UPDATE numbers
SET priority = COALESCE($1, priority),
    active   = COALESCE($2, active),
    e164     = COALESCE($7, e164),
    type     = COALESCE($8::number_type, type),
    trunk_id = COALESCE($9, trunk_id),
    flow_id  = CASE
                  WHEN $3::boolean THEN NULL
                  WHEN $4::uuid IS NOT NULL THEN $4::uuid
                  ELSE flow_id
               END
WHERE id = $5 AND tenant_id = $6
RETURNING id, tenant_id, e164, type::text, trunk_id, flow_id, COALESCE(provider_did_id, ''), priority, active, created_at;

-- name: delete-number
-- Returns the e164 so the api can drop its cached DID route.
DELETE FROM numbers WHERE id = $1 AND tenant_id = $2 RETURNING e164;

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
    recording_format        = COALESCE($10, recording_format),
    mask_numbers_agents      = COALESCE($11, mask_numbers_agents),
    mask_numbers_managers    = COALESCE($12, mask_numbers_managers),
    mask_numbers_supervisors = COALESCE($13, mask_numbers_supervisors)
WHERE id = $3
RETURNING id, name, sip_domain, outbound_strategy, failover_causes, hold_music, record_calls, max_call_seconds, transfer_failed_message, recording_sample_rate, recording_format,
          mask_numbers_agents, mask_numbers_managers, mask_numbers_supervisors;

-- name: get-tenant-settings
-- Tenant-level settings the Settings page reads (not carried in the auth context).
SELECT hold_music, record_calls, max_call_seconds, transfer_failed_message, recording_sample_rate, recording_format,
       mask_numbers_agents, mask_numbers_managers, mask_numbers_supervisors
FROM tenants WHERE id = $1;

-- name: get-tenant-mask-numbers
-- Which roles see masked client numbers (internal/mask): read by the api per
-- scoped request and by the gateway (cached) when it rings an agent.
SELECT mask_numbers_agents, mask_numbers_managers, mask_numbers_supervisors FROM tenants WHERE id = $1;

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

-- name: set-call-recording-s3
-- Mark a recording as stored in S3 (after a successful upload).
UPDATE calls SET recording_s3_key = $2 WHERE leg_id = $1;

-- name: get-tenant-audio
-- The tenant's system hold music and the synthesized "no one answered" prompt
-- path (played on a failed blind transfer; "" when unset — TTS disabled at
-- save). Read by the gateway on the pion plane.
SELECT hold_music, COALESCE(transfer_failed_audio, '') FROM tenants WHERE id = $1;

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
SELECT g.hour, COALESCE(c.n, 0), COALESCE(b.n, 0)
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
LEFT JOIN (
  SELECT date_trunc('hour', enqueued_at) AS hour, count(*) AS n
  FROM queue_stints
  WHERE tenant_id = $1 AND enqueued_at >= date_trunc('hour', now()) - interval '11 hours'
    AND sla_seconds > 0
    AND EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - enqueued_at)) > sla_seconds
  GROUP BY 1
) b ON b.hour = g.hour
ORDER BY g.hour;

-- name: stats-queue-today
-- Today's SLA breaches and average queue wait from queue_stints (a stint is
-- one caller's time in one team queue). Breach = sla_seconds > 0 and the stint
-- (open ones measured to now) outlasted it, whatever the outcome. Avg wait is
-- over answered stints only; 0 when none.
SELECT
  count(*) FILTER (WHERE sla_seconds > 0
                     AND EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - enqueued_at)) > sla_seconds),
  COALESCE(avg(EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - enqueued_at)))
             FILTER (WHERE outcome = 'answered'), 0)::float8
FROM queue_stints
WHERE tenant_id = $1 AND enqueued_at >= date_trunc('day', now());

-- ===========================================================================
-- calls
-- ===========================================================================

-- name: get-agent-sip-username
SELECT sip_username FROM users WHERE id = $1 AND tenant_id = $2;

-- name: get-tenant-sip-domain
SELECT sip_domain FROM tenants WHERE id = $1;

-- name: create-call-outbound
-- $7 is the pion room id (media plane) so a supervisor can be routed to the
-- outbound call's live room; NULL for FreeSWITCH-path outbound calls, which
-- have no pion room.
INSERT INTO calls (tenant_id, direction, status, from_number, to_number, agent_id, leg_id, job_uuid, source, room_id)
VALUES ($1, 'outbound'::call_direction, 'ringing'::call_status, $2, $3, $4, $5, NULLIF($6,''), 'phone'::call_source, NULLIF($7, ''))
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

-- name: call-in-teams
-- Whether a call belongs to any of the teams in $3: it was last rung via one
-- (calls.team) or it waited in one's queue (a queue_stints row). Gates a
-- manager's supervise and recording access; membership of the answering agent
-- deliberately doesn't count.
SELECT EXISTS (
  SELECT 1 FROM calls c
  WHERE c.tenant_id = $1 AND c.id = $2
    AND (c.team = ANY($3::text[])
         OR EXISTS (SELECT 1 FROM queue_stints qs
                    WHERE qs.tenant_id = $1 AND qs.call_id = $2 AND qs.team = ANY($3::text[]))));

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

-- name: get-agent-by-sip
-- Resolve an agent id + display label from a SIP username, used to attribute
-- the answered transfer leg. Label falls back to the SIP username.
SELECT id, COALESCE(NULLIF(display_name, ''), sip_username)
FROM users WHERE tenant_id = $1 AND sip_username = $2;

-- name: get-call
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, '')
FROM calls c LEFT JOIN users a ON a.id = c.agent_id
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
FROM calls c LEFT JOIN users a ON a.id = c.agent_id
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
-- The 4 h started_at window (same cap as list-open-stints) turns this into a
-- short range scan on calls (tenant_id, started_at); without it the planner
-- can walk the tenant's whole call history to find the few open rows (slow on a
-- cold cache, and this runs on every dashboard connect). It also hides CDRs
-- orphaned by a crash that never got an ended_at. A rolling window, not
-- midnight, so a call spanning midnight stays visible.
-- The trailing team / enqueued_at / sla_seconds are the call's open queue stint
-- (newest, within the same 4 h cap as list-open-stints), NULL when not queued.
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, ''),
       qs.team, qs.enqueued_at, qs.sla_seconds
FROM calls c LEFT JOIN users a ON a.id = c.agent_id
LEFT JOIN LATERAL (
    SELECT team, enqueued_at, sla_seconds FROM queue_stints
    WHERE tenant_id = c.tenant_id AND call_id = c.id AND ended_at IS NULL
      AND enqueued_at > now() - interval '4 hours'
    ORDER BY enqueued_at DESC LIMIT 1
) qs ON true
WHERE c.tenant_id = $1 AND c.started_at > now() - interval '4 hours'
  AND c.ended_at IS NULL AND c.status IN ('ringing','answered')
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
SELECT id, sip_password FROM users WHERE tenant_id = $1 AND sip_username = $2;

-- ===========================================================================
-- console auth
-- ===========================================================================

-- name: get-user-by-email
-- The login is the email; password_hash may be NULL (SSO-only user).
SELECT id, tenant_id, password_hash FROM users WHERE lower(email) = $1;

-- name: get-console-principal
-- A console session's user, reloaded every request: role and team memberships
-- drive authorization, and no row (deleted, moved tenant, login removed)
-- revokes the session, as does a session_epoch newer than the token's. Empty arrays, never NULL, for a user in no team.
SELECT u.role, u.sip_username, COALESCE(u.email, ''), COALESCE(u.display_name, ''),
       COALESCE(array_agg(ta.team_name ORDER BY ta.team_name) FILTER (WHERE ta.team_name IS NOT NULL), '{}')::text[],
       COALESCE(array_agg(ta.team_name ORDER BY ta.team_name) FILTER (WHERE ta.is_manager), '{}')::text[],
       u.session_epoch
FROM users u
LEFT JOIN team_agents ta ON ta.agent_id = u.id AND ta.tenant_id = u.tenant_id
WHERE u.id = $1 AND u.tenant_id = $2 AND u.email IS NOT NULL
GROUP BY u.id;

-- name: get-user-password-hash
SELECT password_hash FROM users WHERE tenant_id = $1 AND id = $2 AND password_hash IS NOT NULL;

-- name: set-user-password
-- Bumps session_epoch so other sessions die; the caller gets a fresh token.
UPDATE users SET password_hash = $3, session_epoch = session_epoch + 1
WHERE tenant_id = $1 AND id = $2
RETURNING session_epoch;

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
INSERT INTO api_keys (tenant_id, name, key_hash, key_prefix, scope)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at;

-- name: list-api-keys
-- Never returns the hash/plaintext — only the display prefix + status.
SELECT id, name, key_prefix, scope, created_at, last_used_at, revoked_at
FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC;

-- name: revoke-api-key
-- Soft revoke (keeps the audit row). Scoped to tenant; no-op if already revoked.
UPDATE api_keys SET revoked_at = now()
WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL;

-- name: resolve-api-key-tenant
-- Auth path: map a presented key's hash to its tenant + scope, unless revoked.
SELECT tenant_id, scope FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL;

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

-- name: list-flow-inbound-numbers
-- Inbound DIDs routed to a flow. Read before delete-flow (the FK nulls their
-- flow_id) so the api can drop their cached DID routes.
SELECT e164 FROM numbers WHERE flow_id = $1 AND tenant_id = $2 AND type = 'inbound';

-- name: get-flow-definition
SELECT definition::text FROM flows WHERE id = $1 AND tenant_id = $2;

-- ===========================================================================
-- agent token / sdk credentials
-- ===========================================================================

-- ===========================================================================
-- webhooks
-- ===========================================================================

-- name: create-webhook
INSERT INTO webhooks (tenant_id, name, url, secret, headers, active)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, tenant_id, name, url, secret, headers, active, created_at, updated_at;

-- name: list-webhooks
SELECT id, tenant_id, name, url, headers, active, created_at, updated_at
FROM webhooks WHERE tenant_id = $1 ORDER BY created_at DESC;

-- name: update-webhook
-- NULL leaves a field unchanged.
UPDATE webhooks SET
    name       = COALESCE($3, name),
    url        = COALESCE($4, url),
    headers    = COALESCE($5, headers),
    active     = COALESCE($6, active),
    updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, name, url, headers, active, created_at, updated_at;

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

-- ===========================================================================
-- SIP password encryption at rest (internal/secretbox, api startup)
-- Sealed values carry the 'enc:v1:' prefix; anything else non-empty is
-- legacy plaintext.
-- ===========================================================================

-- name: count-stored-secrets
-- (sealed, plaintext) SIP password counts across trunks + agents.
SELECT count(*) FILTER (WHERE sip_password LIKE 'enc:v1:%'),
       count(*) FILTER (WHERE sip_password <> '' AND sip_password NOT LIKE 'enc:v1:%')
FROM (SELECT sip_password FROM trunks UNION ALL SELECT sip_password FROM users) s;

-- name: list-plaintext-trunk-secrets
-- Locked so concurrent api replicas starting together seal each row once.
SELECT id, sip_password FROM trunks
WHERE sip_password <> '' AND sip_password NOT LIKE 'enc:v1:%'
FOR UPDATE;

-- name: set-trunk-secret
UPDATE trunks SET sip_password = $2 WHERE id = $1;

-- name: list-plaintext-agent-secrets
SELECT id, sip_password FROM users
WHERE sip_password <> '' AND sip_password NOT LIKE 'enc:v1:%'
FOR UPDATE;

-- name: set-agent-secret
UPDATE users SET sip_password = $2 WHERE id = $1;

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
-- Call settlement (internal/callcdr)
-- ===========================================================================

-- name: mark-call-answered
UPDATE calls
SET status = 'answered'::call_status, answered_at = now()
WHERE leg_id = $1 AND status = 'ringing'::call_status
RETURNING id;

-- name: settle-call
UPDATE calls
SET status = $1::call_status, ended_at = now(),
    duration_seconds = $2, hangup_cause = $3
WHERE leg_id = $4 AND status NOT IN ('ended','failed');

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
-- DEFINITION is loaded separately by id (get-active-flow-by-id), so the two
-- are cached under separate keys: a number's flow assignment can change
-- independently of the flow's contents (see flowstore).
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
INSERT INTO users (tenant_id, email, password_hash, sip_username, sip_password, role)
VALUES ($1, lower($2), $3, lower($2), $4, 'admin');

-- ===========================================================================
-- webhook notifier (shared: api + orchestrator)
-- ===========================================================================

-- name: list-tenant-webhooks
-- Every webhook of a tenant, for the notifier's per-tenant cache.
SELECT id, url, secret, headers, active
FROM webhooks
WHERE tenant_id = $1;

-- name: insert-webhook-delivery
INSERT INTO webhook_deliveries (webhook_id, event_type, payload)
VALUES ($1, $2, $3) RETURNING id;

-- name: set-delivery-payload
UPDATE webhook_deliveries SET payload = $1 WHERE id = $2;

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

-- ===========================================================================
-- /v1/me (the logged-in agent's own view)
-- ===========================================================================

-- name: get-me-stats-today
-- $1 tenant, $2 user. A call is the user's when they answered it: an
-- 'answered' event naming them (callcdr re-stamps calls.agent_id on transfer,
-- so it only names the last agent), or calls.agent_id for paths that log no
-- answered event (outbound originate). taken = such calls; talk = their full
-- duration_seconds, an approximation for a call handed off mid-way; missed =
-- ended calls that rang the user and that they never answered, one per call
-- however many times it rang them. Events of a call in the window happen after
-- it started, so bounding e.at keeps the event lookups on the (tenant, type,
-- at) index. The window starts at the DB session's local midnight, like
-- stats-today (Go-side tz math has drifted from it before).
SELECT
  (SELECT count(*) FROM calls c
    WHERE c.tenant_id = $1 AND c.started_at >= date_trunc('day', now()) AND c.answered_at IS NOT NULL
      AND (c.agent_id = $2 OR c.id IN (SELECT e.call_id FROM call_events e
         WHERE e.tenant_id = $1 AND e.type = 'answered' AND e.detail->>'agent_id' = $2::text AND e.at >= date_trunc('day', now())))),
  (SELECT COALESCE(sum(c.duration_seconds), 0)::int FROM calls c
    WHERE c.tenant_id = $1 AND c.started_at >= date_trunc('day', now()) AND c.answered_at IS NOT NULL
      AND (c.agent_id = $2 OR c.id IN (SELECT e.call_id FROM call_events e
         WHERE e.tenant_id = $1 AND e.type = 'answered' AND e.detail->>'agent_id' = $2::text AND e.at >= date_trunc('day', now())))),
  (SELECT count(*) FROM calls c
    WHERE c.tenant_id = $1 AND c.started_at >= date_trunc('day', now()) AND c.ended_at IS NOT NULL
      AND c.agent_id IS DISTINCT FROM $2
      AND c.id IN (SELECT e.call_id FROM call_events e
         WHERE e.tenant_id = $1 AND e.type = 'ring' AND e.detail->>'agent_id' = $2::text AND e.at >= date_trunc('day', now()))
      AND NOT EXISTS (SELECT 1 FROM call_events e
         WHERE e.tenant_id = $1 AND e.call_id = c.id AND e.type = 'answered' AND e.detail->>'agent_id' = $2::text));

-- name: get-me-stats-7d
-- Same as get-me-stats-today over a rolling 7 days.
SELECT
  (SELECT count(*) FROM calls c
    WHERE c.tenant_id = $1 AND c.started_at >= now() - interval '7 days' AND c.answered_at IS NOT NULL
      AND (c.agent_id = $2 OR c.id IN (SELECT e.call_id FROM call_events e
         WHERE e.tenant_id = $1 AND e.type = 'answered' AND e.detail->>'agent_id' = $2::text AND e.at >= now() - interval '7 days'))),
  (SELECT COALESCE(sum(c.duration_seconds), 0)::int FROM calls c
    WHERE c.tenant_id = $1 AND c.started_at >= now() - interval '7 days' AND c.answered_at IS NOT NULL
      AND (c.agent_id = $2 OR c.id IN (SELECT e.call_id FROM call_events e
         WHERE e.tenant_id = $1 AND e.type = 'answered' AND e.detail->>'agent_id' = $2::text AND e.at >= now() - interval '7 days'))),
  (SELECT count(*) FROM calls c
    WHERE c.tenant_id = $1 AND c.started_at >= now() - interval '7 days' AND c.ended_at IS NOT NULL
      AND c.agent_id IS DISTINCT FROM $2
      AND c.id IN (SELECT e.call_id FROM call_events e
         WHERE e.tenant_id = $1 AND e.type = 'ring' AND e.detail->>'agent_id' = $2::text AND e.at >= now() - interval '7 days')
      AND NOT EXISTS (SELECT 1 FROM call_events e
         WHERE e.tenant_id = $1 AND e.call_id = c.id AND e.type = 'answered' AND e.detail->>'agent_id' = $2::text));

-- name: list-my-calls
-- Same columns as list-calls plus `missed`: the user's own calls that started
-- within $5 days before the DB session's local midnight (0 = today). Taken =
-- they answered it (same ownership rule as get-me-stats-today, so a handed-off
-- call stays listed); missed = it rang them, ended, and they never answered
-- it (stats-me-today's rule). Events of a call follow its start, so the same
-- bound keeps the event lookup on the (tenant, type, at) index.
WITH ev AS (
  SELECT e.call_id, e.type FROM call_events e
  WHERE e.tenant_id = $1 AND e.type IN ('ring', 'answered')
    AND e.detail->>'agent_id' = $2::uuid::text
    AND e.at >= date_trunc('day', now()) - make_interval(days => $5::int)
)
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, ''),
       NOT (c.agent_id IS NOT DISTINCT FROM $2 OR c.id IN (SELECT call_id FROM ev WHERE type = 'answered'))
FROM calls c LEFT JOIN users a ON a.id = c.agent_id AND a.tenant_id = c.tenant_id
WHERE c.tenant_id = $1
  AND c.started_at >= date_trunc('day', now()) - make_interval(days => $5::int)
  AND (c.agent_id = $2
       OR c.id IN (SELECT call_id FROM ev WHERE type = 'answered')
       OR (c.ended_at IS NOT NULL AND c.id IN (SELECT call_id FROM ev WHERE type = 'ring')))
ORDER BY c.started_at DESC LIMIT $3 OFFSET $4;

-- name: count-my-calls
-- list-my-calls' total; $3 = days, as list-my-calls' $5.
WITH ev AS (
  SELECT e.call_id, e.type FROM call_events e
  WHERE e.tenant_id = $1 AND e.type IN ('ring', 'answered')
    AND e.detail->>'agent_id' = $2::uuid::text
    AND e.at >= date_trunc('day', now()) - make_interval(days => $3::int)
)
SELECT count(*) FROM calls c
WHERE c.tenant_id = $1
  AND c.started_at >= date_trunc('day', now()) - make_interval(days => $3::int)
  AND (c.agent_id = $2
       OR c.id IN (SELECT call_id FROM ev WHERE type = 'answered')
       OR (c.ended_at IS NOT NULL AND c.id IN (SELECT call_id FROM ev WHERE type = 'ring')));

-- name: list-active-calls-scoped
-- list-active-calls for a non-admin's dashboard ($2 user, $3 teams): calls the
-- user is on (agent_id) plus calls belonging to the teams in $3 (same rule as
-- call-in-teams: last rung via one, or queued in one). A plain agent passes no
-- teams, so they never see another caller's number. Same 4 h window as
-- list-active-calls, which also bounds the stint lookup.
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, ''),
       qs.team, qs.enqueued_at, qs.sla_seconds
FROM calls c LEFT JOIN users a ON a.id = c.agent_id
LEFT JOIN LATERAL (
    SELECT team, enqueued_at, sla_seconds FROM queue_stints
    WHERE tenant_id = c.tenant_id AND call_id = c.id AND ended_at IS NULL
      AND enqueued_at > now() - interval '4 hours'
    ORDER BY enqueued_at DESC LIMIT 1
) qs ON true
WHERE c.tenant_id = $1 AND c.started_at > now() - interval '4 hours'
  AND c.ended_at IS NULL AND c.status IN ('ringing','answered')
  AND (c.agent_id = $2
       OR c.team = ANY($3::text[])
       OR EXISTS (SELECT 1 FROM queue_stints s
                  WHERE s.tenant_id = $1 AND s.call_id = c.id AND s.team = ANY($3::text[])
                    AND s.enqueued_at > now() - interval '4 hours'))
ORDER BY c.started_at ASC LIMIT 100;

-- name: stats-me-today
-- stats-today for one user ($2): calls started today that rang them or that
-- they answered. took/missed use get-me-stats-today's ownership rules (an
-- 'answered' event naming them or calls.agent_id; missed = rang them, ended,
-- never answered by them). Columns: total, took, missed, avg wait (start to
-- answer) and avg talk (answer to end) over the calls they took.
WITH ev AS (
  SELECT e.call_id, e.type FROM call_events e
  WHERE e.tenant_id = $1 AND e.type IN ('ring', 'answered')
    AND e.detail->>'agent_id' = $2::uuid::text AND e.at >= date_trunc('day', now())
)
SELECT count(*),
       count(*) FILTER (WHERE took),
       count(*) FILTER (WHERE missed),
       COALESCE(avg(EXTRACT(EPOCH FROM (answered_at - started_at))) FILTER (WHERE took), 0)::float8,
       COALESCE(avg(EXTRACT(EPOCH FROM (ended_at - answered_at))) FILTER (WHERE took AND ended_at IS NOT NULL), 0)::float8
FROM (
  SELECT c.started_at, c.answered_at, c.ended_at,
         c.answered_at IS NOT NULL
           AND (c.agent_id = $2 OR c.id IN (SELECT call_id FROM ev WHERE type = 'answered')) AS took,
         c.ended_at IS NOT NULL AND c.agent_id IS DISTINCT FROM $2
           AND c.id IN (SELECT call_id FROM ev WHERE type = 'ring')
           AND c.id NOT IN (SELECT call_id FROM ev WHERE type = 'answered') AS missed
  FROM calls c
  WHERE c.tenant_id = $1 AND c.started_at >= date_trunc('day', now())
    AND (c.agent_id = $2 OR c.id IN (SELECT call_id FROM ev))
) m;

-- name: stats-queue-today-teams
-- stats-queue-today limited to the teams in $2.
SELECT
  count(*) FILTER (WHERE sla_seconds > 0
                     AND EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - enqueued_at)) > sla_seconds),
  COALESCE(avg(EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - enqueued_at)))
             FILTER (WHERE outcome = 'answered'), 0)::float8
FROM queue_stints
WHERE tenant_id = $1 AND team = ANY($2::text[]) AND enqueued_at >= date_trunc('day', now());

-- name: stats-me-hourly
-- stats-hourly for one user ($2): their calls (as in stats-me-today) per hour
-- over the last 12 h, and SLA breaches of the teams in $3. Always 12 rows.
SELECT g.hour, COALESCE(c.n, 0), COALESCE(b.n, 0)
FROM generate_series(
       date_trunc('hour', now()) - interval '11 hours',
       date_trunc('hour', now()),
       interval '1 hour'
     ) AS g(hour)
LEFT JOIN (
  SELECT date_trunc('hour', c.started_at) AS hour, count(*) AS n
  FROM calls c
  WHERE c.tenant_id = $1 AND c.started_at >= date_trunc('hour', now()) - interval '11 hours'
    AND (c.agent_id = $2 OR c.id IN (SELECT e.call_id FROM call_events e
           WHERE e.tenant_id = $1 AND e.type IN ('ring', 'answered') AND e.detail->>'agent_id' = $2::text
             AND e.at >= date_trunc('hour', now()) - interval '11 hours'))
  GROUP BY 1
) c ON c.hour = g.hour
LEFT JOIN (
  SELECT date_trunc('hour', enqueued_at) AS hour, count(*) AS n
  FROM queue_stints
  WHERE tenant_id = $1 AND team = ANY($3::text[])
    AND enqueued_at >= date_trunc('hour', now()) - interval '11 hours'
    AND sla_seconds > 0
    AND EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - enqueued_at)) > sla_seconds
  GROUP BY 1
) b ON b.hour = g.hour
ORDER BY g.hour;

-- name: is-my-call
SELECT EXISTS (SELECT 1 FROM calls c WHERE c.tenant_id = $1 AND c.id = $2
  AND (c.agent_id = $3 OR c.id IN (SELECT e.call_id FROM call_events e
         WHERE e.tenant_id = $1 AND e.type = 'answered' AND e.detail->>'agent_id' = $3::text)));

-- ===========================================================================
-- admin users API (/v1/users)
-- ===========================================================================

-- name: list-users
-- Team arrays are empty, never NULL, for a user in no team.
SELECT u.id, COALESCE(u.display_name, ''), COALESCE(u.email, ''), u.role, u.sip_username, u.available_for_calls,
       COALESCE(array_agg(ta.team_name ORDER BY ta.team_name) FILTER (WHERE ta.team_name IS NOT NULL), '{}')::text[],
       COALESCE(array_agg(ta.team_name ORDER BY ta.team_name) FILTER (WHERE ta.is_manager), '{}')::text[],
       (u.email IS NOT NULL), u.created_at
FROM users u
LEFT JOIN team_agents ta ON ta.agent_id = u.id AND ta.tenant_id = u.tenant_id
WHERE u.tenant_id = $1
GROUP BY u.id
ORDER BY u.created_at, u.id;

-- name: get-user
SELECT u.id, COALESCE(u.display_name, ''), COALESCE(u.email, ''), u.role, u.sip_username, u.available_for_calls,
       COALESCE(array_agg(ta.team_name ORDER BY ta.team_name) FILTER (WHERE ta.team_name IS NOT NULL), '{}')::text[],
       COALESCE(array_agg(ta.team_name ORDER BY ta.team_name) FILTER (WHERE ta.is_manager), '{}')::text[],
       (u.email IS NOT NULL), u.created_at
FROM users u
LEFT JOIN team_agents ta ON ta.agent_id = u.id AND ta.tenant_id = u.tenant_id
WHERE u.tenant_id = $1 AND u.id = $2
GROUP BY u.id;

-- name: create-user
INSERT INTO users (tenant_id, email, password_hash, sip_username, sip_password, display_name, role)
VALUES ($1, lower($2), $3, $4, $5, NULLIF($6, ''), $7)
RETURNING id;

-- name: update-user
-- NULL parameter = leave unchanged. Clearing a login is clear-user-login.
-- A new password or a changed email revokes the user's sessions (epoch bump);
-- SET expressions see the old row, so the email comparison is old vs new.
UPDATE users SET
    display_name  = COALESCE($3, display_name),
    email         = COALESCE(lower($4), email),
    role          = COALESCE($5, role),
    password_hash = COALESCE($6, password_hash),
    session_epoch = session_epoch +
        CASE WHEN $6 IS NOT NULL OR ($4 IS NOT NULL AND lower($4) IS DISTINCT FROM email) THEN 1 ELSE 0 END
WHERE tenant_id = $1 AND id = $2
RETURNING id;

-- name: clear-user-login
UPDATE users SET email = NULL, password_hash = NULL, session_epoch = session_epoch + 1
WHERE tenant_id = $1 AND id = $2
RETURNING id;

-- name: rename-user-sip
UPDATE users SET sip_username = $3 WHERE tenant_id = $1 AND id = $2;

-- name: user-on-call
-- Whether the user is connected to a live call (blocks a SIP rename). Same 4 h
-- window as list-active-calls, so a CDR orphaned by a crash can't block forever.
SELECT EXISTS (
    SELECT 1 FROM calls
    WHERE tenant_id = $1 AND agent_id = $2 AND status = 'answered'
      AND ended_at IS NULL AND started_at > now() - interval '4 hours'
);

-- name: flows-by-agent-sip
-- Flows with a transfer node pinned to the agent $2 (config.agent_sip), listed
-- after a SIP rename so the admin can fix them; flows aren't rewritten.
SELECT id, name FROM flows
WHERE tenant_id = $1
  AND jsonb_path_exists(definition,
      '$.nodes[*] ? (@.type == "transfer" && @.config.agent_sip == $sip)',
      jsonb_build_object('sip', $2::text))
ORDER BY name;

-- name: delete-user
DELETE FROM users WHERE tenant_id = $1 AND id = $2 RETURNING sip_username;

-- ===========================================================================
-- team views (/v1/my-teams)
-- ===========================================================================

-- name: set-call-team
-- Attribute a call to the team now ringing it; the last team wins on transfer
-- (queue_stints keeps the full history).
UPDATE calls SET team = $3 WHERE id = $2 AND tenant_id = $1;

-- name: list-team-memberships
-- Every team membership with the member's identity and manager flag, for the
-- team views; live status is joined from presence by the caller.
SELECT ta.team_name, u.id, u.sip_username, COALESCE(u.display_name, ''), ta.is_manager
FROM team_agents ta JOIN users u ON u.id = ta.agent_id AND u.tenant_id = ta.tenant_id
WHERE ta.tenant_id = $1
ORDER BY ta.team_name, ta.position, u.sip_username;

-- name: list-team-candidates
-- Tenant users not yet in team $2: who a team manager can add.
SELECT u.id, COALESCE(u.display_name, ''), u.sip_username
FROM users u
WHERE u.tenant_id = $1
  AND NOT EXISTS (SELECT 1 FROM team_agents ta
                  WHERE ta.tenant_id = u.tenant_id AND ta.team_name = $2 AND ta.agent_id = u.id)
ORDER BY u.sip_username;

-- name: list-team-calls-today
-- Same columns as list-calls: today's calls belonging to team $2 (same
-- predicate as call-in-teams). A stint for a call started today is enqueued
-- today, so the stint subquery is bounded the same way and evaluated once
-- rather than per call. The LIMIT is a backstop for a very busy team.
SELECT c.id, c.tenant_id, c.direction::text, c.status::text, c.from_number, c.to_number,
       c.agent_id, c.leg_id, c.started_at, c.answered_at, c.ended_at,
       c.duration_seconds, c.hangup_cause, c.recording_url, c.ivr_path,
       COALESCE(NULLIF(a.display_name, ''), a.sip_username), c.client_ref, c.source::text,
       COALESCE(c.room_id, '')
FROM calls c LEFT JOIN users a ON a.id = c.agent_id
WHERE c.tenant_id = $1 AND c.started_at >= date_trunc('day', now())
  AND (c.team = $2
       OR c.id IN (SELECT call_id FROM queue_stints
                   WHERE tenant_id = $1 AND team = $2 AND enqueued_at >= date_trunc('day', now())))
ORDER BY c.started_at DESC LIMIT 500;

-- name: team-stats-today
-- Today's queue stints for team $2: offered (stints entered), answered,
-- abandoned (caller hung up or the max wait ran out) and average wait over
-- answered stints (0 when none). Same wait measure as stats-queue-today.
SELECT count(*),
       count(*) FILTER (WHERE outcome = 'answered'),
       count(*) FILTER (WHERE outcome IN ('abandoned', 'timeout')),
       COALESCE(avg(EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - enqueued_at)))
                  FILTER (WHERE outcome = 'answered'), 0)::float8
FROM queue_stints
WHERE tenant_id = $1 AND team = $2 AND enqueued_at >= date_trunc('day', now());

-- name: remove-team-agent-nonmanager
-- remove-team-agent for a team manager: a manager can't remove a manager
-- (themselves included), or they could strip and re-add a co-manager as a
-- plain member. 0 rows: not a member, or a manager (see get-team-member-manager).
DELETE FROM team_agents
WHERE tenant_id = $1 AND team_name = $2 AND agent_id = $3 AND NOT is_manager;

-- name: get-team-member-manager
-- A member's manager flag; no row when the user isn't in the team.
SELECT is_manager FROM team_agents WHERE tenant_id = $1 AND team_name = $2 AND agent_id = $3;

-- name: set-team-agent-manager
-- Set or clear a member's manager flag; 0 rows when the user isn't a member.
UPDATE team_agents SET is_manager = $4
WHERE tenant_id = $1 AND team_name = $2 AND agent_id = $3;

-- ===========================================================================
-- api: /v1/sso-providers (admin) and SSO login routing
-- ===========================================================================

-- name: list-sso-providers
-- sso_only_users: users with no password whose email domain is one of the
-- provider's — they lose their login if the provider is removed.
SELECT p.id, p.name, p.issuer_url, p.client_id, p.require_sso, p.enabled, p.created_at,
       COALESCE((SELECT array_agg(d.domain ORDER BY d.domain) FROM sso_provider_domains d
                 WHERE d.provider_id = p.id AND d.tenant_id = p.tenant_id), '{}')::text[],
       p.require_hosted_domain,
       (SELECT count(*) FROM users u
         WHERE u.tenant_id = p.tenant_id AND u.email IS NOT NULL AND u.password_hash IS NULL
           AND split_part(lower(u.email), '@', 2) IN
               (SELECT d.domain FROM sso_provider_domains d WHERE d.provider_id = p.id AND d.tenant_id = p.tenant_id))::int
FROM sso_providers p
WHERE p.tenant_id = $1
ORDER BY p.created_at, p.id;

-- name: get-sso-provider
-- Includes the sealed secret: callers open it with Server.openSecret.
SELECT p.id, p.name, p.issuer_url, p.client_id, p.client_secret, p.require_sso, p.enabled, p.created_at,
       COALESCE((SELECT array_agg(d.domain ORDER BY d.domain) FROM sso_provider_domains d
                 WHERE d.provider_id = p.id AND d.tenant_id = p.tenant_id), '{}')::text[],
       p.require_hosted_domain
FROM sso_providers p
WHERE p.tenant_id = $1 AND p.id = $2;

-- name: create-sso-provider
INSERT INTO sso_providers (tenant_id, name, issuer_url, client_id, client_secret, require_sso, enabled, require_hosted_domain)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: update-sso-provider
-- NULL = unchanged.
UPDATE sso_providers
SET name          = COALESCE($3, name),
    issuer_url    = COALESCE($4, issuer_url),
    client_id     = COALESCE($5, client_id),
    client_secret = COALESCE($6, client_secret),
    require_sso   = COALESCE($7, require_sso),
    enabled       = COALESCE($8, enabled),
    require_hosted_domain = COALESCE($9, require_hosted_domain),
    updated_at    = now()
WHERE tenant_id = $1 AND id = $2;

-- name: delete-sso-provider
DELETE FROM sso_providers WHERE tenant_id = $1 AND id = $2;

-- name: delete-sso-provider-domains
DELETE FROM sso_provider_domains WHERE tenant_id = $1 AND provider_id = $2;

-- name: insert-sso-provider-domain
INSERT INTO sso_provider_domains (domain, provider_id, tenant_id) VALUES ($1, $2, $3);

-- name: get-sso-domain
-- Login routing by email domain alone (no tenant yet); enabled providers only.
SELECT p.id, p.tenant_id, p.issuer_url, p.client_id, p.client_secret, p.require_sso
FROM sso_provider_domains d
JOIN sso_providers p ON p.id = d.provider_id
WHERE d.domain = $1 AND p.enabled;

-- name: get-single-sso-provider
-- The login page's "Sign in with <provider>" button, before any tenant is
-- known (same pre-auth global exception as get-sso-domain): the enabled
-- provider, only when exactly one is enabled install-wide.
SELECT id, tenant_id, name, issuer_url, client_id
FROM sso_providers
WHERE enabled AND (SELECT count(*) FROM sso_providers WHERE enabled) = 1;

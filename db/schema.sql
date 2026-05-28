-- Tring v0 schema. Multi-tenant from day one — every row except `tenants`
-- carries a tenant_id, and queries MUST filter by it. Application-level
-- enforcement (auth middleware sets tenant_id from API key, never trusts a
-- request-supplied value). Row-level security policies will follow once the
-- query layer is sqlc-generated.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Tenants are the integrating CRMs (or self-host operators acting as one).
-- api_key_hash is SHA-256(plaintext_key). The plaintext is shown to the
-- operator exactly once at creation. api_key_prefix is the first 12 chars
-- of the plaintext key, kept for admin-UI identification (Stripe pattern).
--
-- outbound_strategy controls how multiple outbound trunks are picked per
-- call. Values:
--   'failover'    - bridge to all in priority order; continue_on_fail
--                   constrained to the causes in failover_causes
--   'round_robin' - pick the next one in rotation per call (in-memory)
--   'manual'      - use only the lowest-priority active number; operator
--                   flips `numbers.active` to control fail-over manually
--   'load_based'  - reserved (not implemented in v0.2; runtime returns 501)
CREATE TABLE tenants (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              TEXT NOT NULL,
    api_key_hash      TEXT NOT NULL UNIQUE,
    api_key_prefix    TEXT NOT NULL,
    sip_domain        TEXT NOT NULL UNIQUE,
    outbound_strategy TEXT NOT NULL DEFAULT 'failover',
    failover_causes   TEXT[] NOT NULL DEFAULT ARRAY[
        'NETWORK_OUT_OF_ORDER',
        'GATEWAY_DOWN',
        'USER_NOT_REGISTERED',
        'SUBSCRIBER_ABSENT',
        'NORMAL_TEMPORARY_FAILURE',
        'RECOVERY_ON_TIMER_EXPIRE',
        'NO_ROUTE_DESTINATION'
    ]::TEXT[],
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Agents are SIP identities owned by a tenant. The sip_password is what
-- mod_xml_curl returns in <password> on directory lookups; in v1 we generate
-- it server-side, in v2 it becomes a per-session token issued via JWT.
CREATE TABLE agents (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sip_username  TEXT NOT NULL,
    sip_password  TEXT NOT NULL,
    display_name  TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, sip_username)
);

-- Numbers/DIDs allocated to a tenant. type=outbound is what gets stamped as
-- caller-ID on outbound calls (e.g., the tenant's 1600 number). type=inbound
-- is what customers dial. type=masking is the pool for two-way anonymized
-- conversations.
CREATE TYPE number_type AS ENUM ('inbound', 'outbound', 'masking');

CREATE TABLE numbers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    e164            TEXT NOT NULL,
    type            number_type NOT NULL,
    provider        TEXT NOT NULL,           -- 'exotel', 'airtel', 'jio', 'tata', 'loopback'
    provider_did_id TEXT,                    -- provider-side identifier
    priority        INT NOT NULL DEFAULT 100, -- lower = preferred; failover/manual use this ordering
    active          BOOLEAN NOT NULL DEFAULT true, -- manual kill switch; PATCH this off to take a trunk out of rotation without deleting
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, e164)
);

CREATE INDEX ON numbers (tenant_id, type);
CREATE INDEX ON numbers (tenant_id, type, priority ASC) WHERE active;

-- Calls. One row per call leg group (parent UUID); leg-level detail can be
-- added later if we need it for billing reconciliation.
CREATE TYPE call_direction AS ENUM ('inbound', 'outbound');
CREATE TYPE call_status    AS ENUM ('ringing', 'answered', 'ended', 'failed');

CREATE TABLE calls (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    direction        call_direction NOT NULL,
    status           call_status NOT NULL,
    from_number      TEXT NOT NULL,
    to_number        TEXT NOT NULL,
    agent_id         UUID REFERENCES agents(id) ON DELETE SET NULL,
    fs_uuid          TEXT,                   -- FreeSWITCH channel Unique-ID
    fs_job_uuid      TEXT,                   -- bgapi Job-UUID; lets us catch failed-to-create-channel originates via BACKGROUND_JOB events
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at      TIMESTAMPTZ,
    ended_at         TIMESTAMPTZ,
    duration_seconds INTEGER,
    hangup_cause     TEXT,
    recording_url    TEXT
);

CREATE INDEX ON calls (tenant_id, started_at DESC);
CREATE INDEX ON calls (tenant_id, agent_id, started_at DESC);
CREATE INDEX ON calls (fs_uuid);
CREATE INDEX ON calls (fs_job_uuid);

-- Masking sessions back the two-way private-number flow. Reuses the in-memory
-- pool semantics from cmd/orchestrator but persists across restarts.
CREATE TABLE masking_sessions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    masked_did     TEXT NOT NULL,
    party_a        TEXT NOT NULL,
    party_b        TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, masked_did)
);

CREATE INDEX ON masking_sessions (tenant_id, expires_at);

-- Tenant-registered webhooks. Events emitted: call.ringing, call.answered,
-- call.ended. HMAC signature uses `secret` over `<ts>.<body>`.
CREATE TABLE webhooks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    url         TEXT NOT NULL,
    secret      TEXT NOT NULL,
    events      TEXT[] NOT NULL DEFAULT '{}',  -- empty = all
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per delivery attempt sequence. status moves
-- pending -> success | exhausted. Each attempt updates last_error and
-- response_status; final outcome lands in status + delivered_at.
CREATE TYPE webhook_delivery_status AS ENUM (
    'pending', 'success', 'exhausted'
);

CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id      UUID NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL,
    status          webhook_delivery_status NOT NULL DEFAULT 'pending',
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    response_status INTEGER,
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON webhook_deliveries (webhook_id, created_at DESC);

-- ─── IVR Flows ─────────────────────────────────────────────────────
-- An IVR flow is a graph of nodes (greeting, menu, gather, transfer,
-- http_callback, goto_flow, timing, hangup) connected by edges with
-- named source handles (e.g. "digit:1", "timeout", "http:2xx"). The full
-- graph is stored as JSONB in `definition` — the FreeSWITCH executor
-- (Phase 3) walks it on inbound calls. Schema matches the IVRFlowData
-- v2 shape used by the frontend (see web/src/api/types.ts).
CREATE TABLE flows (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    is_active        BOOLEAN NOT NULL DEFAULT true,
    is_call_start    BOOLEAN NOT NULL DEFAULT false,
    is_outgoing_end  BOOLEAN NOT NULL DEFAULT false,
    definition       JSONB NOT NULL DEFAULT '{"version":2,"nodes":[],"edges":[],"entry_node":""}'::jsonb,
    welcome_audio_url TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX flows_tenant_name_idx ON flows (tenant_id, name);

-- Inbound DIDs can point at a flow — when a call lands on the number,
-- the executor enters this flow. NULL = no IVR; the dialplan falls back
-- to whatever default routing applies.
ALTER TABLE numbers
    ADD COLUMN flow_id UUID REFERENCES flows(id) ON DELETE SET NULL;

-- ─── SIP Trunks (per-tenant, UI-managed) ────────────────────────────
-- Replaces the four hardcoded env-based gateways (EXOTEL/AIRTEL/JIO/TATA
-- in vars.xml) with DB-backed, per-tenant trunk definitions. On change,
-- Tring renders gateway XML to freeswitch/conf/sip_profiles/external/
-- dynamic/ and rescans the external profile via ESL — zero downtime.
--
-- Two auth modes are supported, distinguished by the `register` flag:
--   register=true:  classic SIP REGISTER (Exotel-style aggregators).
--                   sip_username + sip_password + sip_realm are used.
--   register=false: IP-authenticated (Tata-private-circuit style).
--                   proxy_ip is the SBC IP; auth_acl_cidr is the SBC
--                   CIDR used to allow inbound INVITEs.
CREATE TABLE trunks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    -- provider_type drives caller-ID normalization in internal/provider
    -- (india / loopback / etc). Distinct from `name`, a free-form label.
    provider_type   TEXT NOT NULL DEFAULT 'generic',
    sip_username    TEXT NOT NULL DEFAULT '',
    sip_password    TEXT NOT NULL DEFAULT '',
    sip_realm       TEXT NOT NULL DEFAULT '',
    register        BOOLEAN NOT NULL DEFAULT false,
    proxy_ip        TEXT NOT NULL DEFAULT '',
    auth_acl_cidr   TEXT NOT NULL DEFAULT '',
    active          BOOLEAN NOT NULL DEFAULT true,
    extra_params    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX trunks_tenant_name_unique ON trunks (tenant_id, name);

-- Numbers reference a specific trunk via trunk_id. The legacy `provider`
-- string column stays for backwards compatibility — if trunk_id is set,
-- routing prefers it; otherwise falls back to env-based provider gateways.
ALTER TABLE numbers
    ADD COLUMN trunk_id UUID REFERENCES trunks(id) ON DELETE SET NULL;

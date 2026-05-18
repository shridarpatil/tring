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
CREATE TABLE tenants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    api_key_hash    TEXT NOT NULL UNIQUE,
    api_key_prefix  TEXT NOT NULL,
    sip_domain      TEXT NOT NULL UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
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
    provider        TEXT NOT NULL,           -- 'exotel', 'plivo', 'raw_sip'
    provider_did_id TEXT,                    -- provider-side identifier
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, e164)
);

CREATE INDEX ON numbers (tenant_id, type);

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

-- Per-tenant API keys (multiple, named — Stripe/Twilio style). Replaces the
-- single deprecated tenants.api_key_hash sentinel: admins issue/revoke real
-- keys here, and AuthRequired authenticates `tk_live_…` bearers against this
-- table. The plaintext is shown once at creation; only the SHA-256 hash is
-- stored. Soft-revoke (revoked_at) keeps the audit row + last_used_at.
CREATE TABLE api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    key_hash     TEXT NOT NULL UNIQUE,
    key_prefix   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX ON api_keys (tenant_id);

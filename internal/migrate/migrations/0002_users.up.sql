-- Console users — operators who log into the Vue admin console.
--
-- Each user belongs to exactly one tenant (multi-tenant orgs get one user
-- row per tenant they administer; v0 doesn't model cross-tenant access).
-- password_hash is bcrypt (cost 10). Email is unique across the whole
-- system, not per-tenant, to keep the login form a single field.
--
-- This is separate from `agents` (SIP identities, dial-pad users). An
-- operator running the console is not a callable SIP endpoint.

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_tenant ON users(tenant_id);

-- OIDC SSO for console logins. A domain belongs to exactly one provider
-- (globally), so the email-first login step can route by domain alone without
-- looking up a user.
CREATE TABLE sso_providers (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    issuer_url    TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL, -- secretbox-sealed
    require_sso   BOOLEAN NOT NULL DEFAULT false,
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sso_providers_tenant_idx ON sso_providers (tenant_id);

CREATE TABLE sso_provider_domains (
    domain      TEXT PRIMARY KEY,
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX sso_provider_domains_provider_idx ON sso_provider_domains (provider_id);

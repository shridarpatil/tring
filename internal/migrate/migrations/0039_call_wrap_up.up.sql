-- Call wrap-up: after a call the agent picks a disposition, adds notes and
-- tags. Settings live on the team (when the call has one) else the tenant.
ALTER TABLE teams
    ADD COLUMN wrap_up_seconds     INT     NOT NULL DEFAULT 0 CHECK (wrap_up_seconds BETWEEN 0 AND 600),
    ADD COLUMN require_disposition BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE tenants
    ADD COLUMN wrap_up_seconds     INT     NOT NULL DEFAULT 0 CHECK (wrap_up_seconds BETWEEN 0 AND 600),
    ADD COLUMN require_disposition BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE dispositions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX dispositions_tenant_name ON dispositions (tenant_id, lower(name));

CREATE TABLE call_tags (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX call_tags_tenant_name ON call_tags (tenant_id, lower(name));

ALTER TABLE calls
    ADD COLUMN disposition_id      UUID REFERENCES dispositions(id),
    ADD COLUMN notes               TEXT,
    ADD COLUMN wrapped_up_at       TIMESTAMPTZ,
    ADD COLUMN wrap_up_end         TEXT CHECK (wrap_up_end IN ('done', 'timeout')),
    ADD COLUMN wrap_up_agent_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    -- require_disposition in force when wrap-up began, so the api's save
    -- enforces the same rule the widget showed even if settings changed since.
    ADD COLUMN wrap_up_require     BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN wrap_up_webhook_id  UUID;
CREATE INDEX calls_tenant_disposition ON calls (tenant_id, disposition_id, started_at DESC);

CREATE TABLE call_tag_links (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    call_id   UUID NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    tag_id    UUID NOT NULL REFERENCES call_tags(id),
    PRIMARY KEY (call_id, tag_id)
);
CREATE INDEX call_tag_links_tenant_tag ON call_tag_links (tenant_id, tag_id);

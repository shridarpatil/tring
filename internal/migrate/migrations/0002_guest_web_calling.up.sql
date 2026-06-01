-- Guest web-to-web calling: customers place WebRTC calls from a website
-- with an ephemeral, anonymous SIP identity that is hard-pinned to a
-- single IVR flow and can never bridge to a trunk (toll-fraud guard).

-- Per-tenant public widget key (Stripe-publishable-key style; safe to
-- embed in a public web page) + the flow guest calls are routed into.
ALTER TABLE tenants ADD COLUMN public_key TEXT UNIQUE;
ALTER TABLE tenants ADD COLUMN support_flow_id UUID REFERENCES flows(id) ON DELETE SET NULL;

-- Backfill a key for existing tenants so the widget works in dev without a
-- manual step. Production tenant creation should mint its own key.
UPDATE tenants
SET public_key = 'pk_live_' || md5(random()::text || clock_timestamp()::text || id::text)
WHERE public_key IS NULL;

-- Ephemeral guest SIP identities. DB-backed (not in-memory) so the
-- mod_xml_curl directory lookup resolves on any API instance. Rows are
-- short-lived; expired rows are reaped lazily on the next mint.
CREATE TABLE guest_sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sip_username TEXT NOT NULL UNIQUE,
    sip_password TEXT NOT NULL,
    flow_id      UUID REFERENCES flows(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX ON guest_sessions (sip_username);
CREATE INDEX ON guest_sessions (expires_at);

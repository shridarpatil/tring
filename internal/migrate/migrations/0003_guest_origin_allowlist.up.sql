-- Guest-calling hardening: bind a tenant's public widget key to the
-- domains it's allowed to be embedded on. The guest-credentials endpoint
-- rejects any request whose Origin isn't in this list (fail-closed: an
-- empty list disables guest minting until the operator configures it).
--
-- This stops a third party from lifting a public pk_live_ key out of page
-- source and embedding the widget on their own site to burn the tenant's
-- channels / agent time. It's a browser-enforced control (Origin can be
-- spoofed by non-browser clients), so it pairs with per-IP rate limiting
-- and the per-tenant concurrent-session cap in the application layer.
ALTER TABLE tenants
    ADD COLUMN allowed_origins TEXT[] NOT NULL DEFAULT '{}'::TEXT[];

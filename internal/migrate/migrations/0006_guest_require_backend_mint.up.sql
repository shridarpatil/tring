-- Per-tenant policy: when true, the PUBLIC guest mint
-- (POST /v1/sdk/guest-credentials, public key) is refused and guest
-- credentials may only be minted server-side via the API-key endpoint
-- (POST /v1/guest-credentials). Lets a tenant require attributable,
-- backend-minted callers (e.g. a logged-in support portal) while leaving the
-- zero-backend anonymous drop-in available by default (false).
ALTER TABLE tenants
    ADD COLUMN guest_require_backend_mint BOOLEAN NOT NULL DEFAULT false;

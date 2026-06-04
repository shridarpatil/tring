-- Disable the public guest mint (/v1/sdk/guest-credentials) BY DEFAULT.
--
-- Security hardening: the public, unauthenticated mint is an easy token source
-- for resource-exhaustion. The safe default is backend-only minting via
-- /v1/guest-credentials (tenant API key). The public endpoint still exists and
-- a tenant can re-enable it per-deployment (guest_require_backend_mint=false)
-- if they genuinely want zero-backend anonymous calling — but it's now opt-IN.
--
-- Flip the column default for new tenants AND bring existing tenants up to the
-- secure posture on upgrade.
ALTER TABLE tenants ALTER COLUMN guest_require_backend_mint SET DEFAULT true;
UPDATE tenants SET guest_require_backend_mint = true;

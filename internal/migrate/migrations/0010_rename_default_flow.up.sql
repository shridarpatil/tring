-- Rename the tenant's default guest flow to a generic name — it's the default
-- flow for ANY guest call (used only as a fallback when a mint doesn't pass an
-- explicit flow_id), not "support"-specific.
ALTER TABLE tenants RENAME COLUMN support_flow_id TO default_flow_id;

-- Drop the public-mint config columns, vestigial since the public mint
-- (/v1/sdk/guest-credentials) was removed — all visitor tokens are now minted
-- server-side via /v1/guest-credentials (tenant API key). Nothing reads these.
ALTER TABLE tenants DROP COLUMN IF EXISTS public_key;
ALTER TABLE tenants DROP COLUMN IF EXISTS allowed_origins;
ALTER TABLE tenants DROP COLUMN IF EXISTS guest_require_backend_mint;

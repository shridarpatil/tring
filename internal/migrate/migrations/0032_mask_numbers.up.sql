-- Per-role switches that mask client phone numbers for console users and the
-- agent widget (internal/mask). Admins, API keys and webhooks are never masked.
ALTER TABLE tenants
    ADD COLUMN mask_numbers_agents      BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN mask_numbers_managers    BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN mask_numbers_supervisors BOOLEAN NOT NULL DEFAULT false;

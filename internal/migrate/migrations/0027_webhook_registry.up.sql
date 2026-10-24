-- Webhooks become a registry that IVR flows pick from: a node's "notify
-- webhook" and a flow's "on call end" webhook reference one by id. Tenant-wide
-- event subscriptions are gone, so events goes. headers is a JSON object of
-- extra request headers, sealed with TRING_SECRETS_KEY like SIP passwords
-- ('' = none). A disabled webhook is kept but never sent.
ALTER TABLE webhooks
    ADD COLUMN name TEXT NOT NULL DEFAULT '',
    ADD COLUMN headers TEXT NOT NULL DEFAULT '',
    ADD COLUMN active BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    DROP COLUMN events;

UPDATE webhooks SET name = url WHERE name = '';

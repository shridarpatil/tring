-- API key scopes. 'full' = everything a console admin can do (every existing
-- key keeps this); 'calls' = what a CRM backend needs to place and handle
-- calls (calls, guest/agent tokens, agent provisioning, read-only lookups) —
-- no key, trunk, webhook, settings, sound, flow or team administration.
ALTER TABLE api_keys
    ADD COLUMN scope TEXT NOT NULL DEFAULT 'full'
    CHECK (scope IN ('full', 'calls'));

-- One identity table for everyone who can log in or take calls.
--
-- Until now `agents` (SIP identities the SDK/gateway key everything on) and
-- console `users` (admins with email + password) were separate, so an agent
-- could not log in and an admin could not be a callable endpoint. `agents` is
-- the table kept and renamed to `users`, because its ids are referenced by
-- calls, team_agents and the call history; console admins are folded into it
-- with role 'admin' and keep their ids (supervisor_sessions points at them).
-- email/password_hash are optional: an agent driven purely by a CRM has neither.
--
-- calls.team records which team a call was attributed to when it was routed.
-- It is attribution, not membership: it must survive an agent leaving a team.

ALTER TABLE agents
    ADD COLUMN email         TEXT,
    ADD COLUMN password_hash TEXT,
    ADD COLUMN role          TEXT NOT NULL DEFAULT 'agent'
        CHECK (role IN ('admin', 'supervisor', 'agent'));

-- Admins need a sip_username (NOT NULL, unique per tenant). Use the email;
-- if an agent in the tenant already owns that name, disambiguate with the id.
-- The random sip_password is plaintext here; the api's startup pass
-- (list-plaintext-agent-secrets) seals it.
INSERT INTO agents (id, tenant_id, sip_username, sip_password, display_name, email, password_hash, role)
SELECT u.id, u.tenant_id,
       CASE WHEN EXISTS (SELECT 1 FROM agents a
                          WHERE a.tenant_id = u.tenant_id
                            AND a.sip_username = left(lower(u.email), 64))
            THEN left(lower(u.email), 55) || '-' || left(u.id::text, 8)
            ELSE left(lower(u.email), 64)
       END,
       gen_random_uuid()::text, NULL, lower(u.email), u.password_hash, 'admin'
FROM users u;

-- The FK would otherwise block dropping the old table; re-added below.
ALTER TABLE supervisor_sessions DROP CONSTRAINT supervisor_sessions_supervisor_user_id_fkey;
DROP TABLE users;
ALTER TABLE agents RENAME TO users;
ALTER TABLE supervisor_sessions
    ADD CONSTRAINT supervisor_sessions_supervisor_user_id_fkey
    FOREIGN KEY (supervisor_user_id) REFERENCES users(id) ON DELETE SET NULL;

-- Login is by email across tenants, case-insensitively.
CREATE UNIQUE INDEX users_email_key ON users (lower(email)) WHERE email IS NOT NULL;

ALTER TABLE team_agents ADD COLUMN is_manager BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE calls ADD COLUMN team TEXT;
CREATE INDEX calls_tenant_team_started_idx ON calls (tenant_id, team, started_at DESC);

-- Missed-call stats count call_events of one type per tenant over a window.
CREATE INDEX call_events_tenant_type_at_idx ON call_events (tenant_id, type, at);

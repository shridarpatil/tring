-- users.session_epoch revokes console sessions on credential changes. Console
-- JWTs are stateless and carry the epoch they were issued at; every request
-- reloads the user, so bumping the epoch (password set/reset, email change,
-- login cleared) makes every older token fail without a revocation list.
ALTER TABLE users ADD COLUMN session_epoch INT NOT NULL DEFAULT 0;

-- Team views attribute a call to a team through queue_stints per call; without
-- this the correlated lookups scan the tenant's whole stint history.
CREATE INDEX IF NOT EXISTS queue_stints_tenant_call ON queue_stints (tenant_id, call_id);

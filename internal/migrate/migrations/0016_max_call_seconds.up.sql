-- Per-tenant maximum call duration (seconds). Used as the ephemeral TURN
-- credential lifetime so the relay credential covers a full-length call.
-- Default 4h — generous enough not to cut a real call short.
ALTER TABLE tenants ADD COLUMN max_call_seconds INTEGER NOT NULL DEFAULT 14400;

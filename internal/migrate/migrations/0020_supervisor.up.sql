-- Supervisor monitor/whisper/barge.

-- The pion room id (<tenant>/<name>) for a media-plane call, so the API can
-- resolve a live call to the room a supervisor must join. leg_id is a generated
-- per-call uuid (recording filename), NOT the room id — hence a dedicated column.
ALTER TABLE calls ADD COLUMN room_id TEXT;

-- Permission gate: only users with can_supervise may join a live call.
ALTER TABLE users ADD COLUMN can_supervise BOOLEAN NOT NULL DEFAULT false;

-- Audit trail: who supervised which call, when, and in which modes. Written by
-- the gateway (best-effort) on supervisor join and on leave.
CREATE TABLE supervisor_sessions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    call_id            UUID REFERENCES calls(id) ON DELETE SET NULL,
    supervisor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    started_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at           TIMESTAMPTZ,
    modes_used         TEXT[] NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_supervisor_sessions_tenant ON supervisor_sessions(tenant_id, started_at DESC);

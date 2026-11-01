-- Per-team staffing / SLA / working-hours settings for the live dashboard,
-- plus queue_stints: one row per time a caller spends waiting in a team queue.
--
--   min_live_agents — staffing alert when fewer live agents than this while
--                     the team is open (0 = no alert)
--   sla_seconds     — max seconds a caller should wait in queue (0 = no SLA).
--                     Passing it never ends the call; it only counts as a breach.
--   hours           — working hours, same JSON shape as the IVR Timing node:
--                     [{day, enabled, start_time, end_time}]; '[]' = always open.
ALTER TABLE teams
    ADD COLUMN min_live_agents INT   NOT NULL DEFAULT 0 CHECK (min_live_agents >= 0),
    ADD COLUMN sla_seconds     INT   NOT NULL DEFAULT 0 CHECK (sla_seconds >= 0),
    ADD COLUMN hours           JSONB NOT NULL DEFAULT '[]';

-- A stint is open while ended_at IS NULL. Breach = sla_seconds > 0 AND
-- COALESCE(ended_at, now()) - enqueued_at > sla_seconds, whatever the outcome.
CREATE TABLE queue_stints (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    call_id     UUID NULL,               -- NULL when the room had no CDR row
    team        TEXT NOT NULL,           -- no FK: history survives team deletion
    sla_seconds INT  NOT NULL DEFAULT 0, -- team SLA snapshotted at enqueue
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ NULL,
    outcome     TEXT NULL                -- answered | abandoned | timeout | transferred
);
CREATE INDEX queue_stints_tenant_enqueued ON queue_stints (tenant_id, enqueued_at);
CREATE INDEX queue_stints_open ON queue_stints (tenant_id) WHERE ended_at IS NULL;

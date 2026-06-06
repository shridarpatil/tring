-- Teams: a tenant-scoped group of agents with a ring strategy and queue
-- settings. The IVR transfer node targets a team by NAME (no surrogate id —
-- the name is the identifier the flow carries, statically or via a channel
-- var set by an http_callback backend lookup). A team is also its own queue:
-- when every member is busy, callers wait (music-on-hold) until one frees up.
--
-- strategy drives ring ordering (handled natively in the orchestrator, NOT
-- mod_callcenter — agents are gateway WebRTC endpoints, not SIP registrations):
--   ring_all     — ring every available member at once (legacy default)
--   round_robin  — rotate the starting member per call
--   sequential   — ring members one at a time by position
--   longest_wait — ring the member idle the longest (uses Redis freed-at)
CREATE TABLE teams (
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    strategy         TEXT NOT NULL DEFAULT 'ring_all',
    ring_seconds     INT  NOT NULL DEFAULT 20,   -- per-attempt ring timeout
    max_wait_seconds INT  NOT NULL DEFAULT 120,  -- max time held in queue
    moh_sound        TEXT NOT NULL DEFAULT 'local_stream://moh',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, name)
);

-- Membership. position orders members for the `sequential` strategy.
CREATE TABLE team_agents (
    tenant_id  UUID NOT NULL,
    team_name  TEXT NOT NULL,
    agent_id   UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    position   INT  NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id, team_name, agent_id),
    FOREIGN KEY (tenant_id, team_name) REFERENCES teams(tenant_id, name) ON DELETE CASCADE
);

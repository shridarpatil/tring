-- Per-call event timeline: an append-only, timestamped, typed log of what
-- happened on a call (started, IVR routing rings, no-answers, answers, agent
-- transfers, supervisor sessions, end). Written incrementally by the gateway
-- (internal/callcdr) at each moment, so the console can show a complete,
-- correctly-ordered history — the routing/answer/transfer story that neither the
-- calls row (final agent only) nor ivr_path (IVR-nodes, batch-stamped) captures.
CREATE TABLE call_events (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    call_id   UUID NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    -- seq is a table-wide monotonic counter used ONLY to break ties when two
    -- events on one call share a timestamp (at has ms resolution); ordering is
    -- (at, seq). It is insertion order, so it is stable and gapless per call.
    seq       BIGINT GENERATED ALWAYS AS IDENTITY,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    type      TEXT NOT NULL,   -- started|ring|no_answer|answered|transfer|supervise|ended
    detail    JSONB NOT NULL DEFAULT '{}'::jsonb
);

-- The one read pattern: every event for a call, in order.
CREATE INDEX call_events_call_idx ON call_events (call_id, at, seq);

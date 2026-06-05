-- available_for_calls is the agent's API-managed availability, separate from
-- connection presence (online/on_call/offline, which lives in Redis). The
-- integrator's backend flips this from their own Away/Break button: the
-- orchestrator rings an agent only when they're BOTH connected (presence) AND
-- available_for_calls. Defaults true so newly-created agents are available.
ALTER TABLE agents ADD COLUMN available_for_calls boolean NOT NULL DEFAULT true;

-- The agent's FreeSWITCH leg (the bridge B-leg on an inbound/IVR transfer).
-- calls.fs_uuid is the CALLER leg; this is the AGENT leg, needed so call control
-- (uuid_hold) acts on the right side — holding the agent leg makes the CUSTOMER
-- hear hold music. NULL for outbound click-to-call, where fs_uuid already IS the
-- agent leg, so control uses COALESCE(agent_fs_uuid, fs_uuid).
ALTER TABLE calls ADD COLUMN agent_fs_uuid TEXT;

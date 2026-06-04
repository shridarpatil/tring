-- Record the IVR journey a call took (node path, DTMF presses, transfer
-- attempts + outcomes) so it shows up in the call log alongside the CDR.
-- An ordered JSON array of steps; shape matches orchestrator.SimStep
-- ({node_id, node_type, action, detail, dtmf_input, outcome}). Defaults to
-- an empty array so existing rows and the outbound path (which doesn't walk
-- a flow) read back as "no IVR path" rather than NULL.
ALTER TABLE calls
    ADD COLUMN ivr_path JSONB NOT NULL DEFAULT '[]'::jsonb;

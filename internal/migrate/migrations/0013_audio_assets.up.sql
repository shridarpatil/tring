-- Per-tenant audio library: operators upload WAV/MP3 once and reuse a file
-- anywhere a sound plays — IVR greeting/menu/gather/hangup prompts and queue
-- hold music. The API writes the bytes into a shared dir FreeSWITCH bind-mounts
-- and reads; fs_path is the absolute path FreeSWITCH's `playback` uses (and what
-- gets stored in a flow node's audio_file / tenants.hold_music). Mirrors the
-- dynamic-gateway shared-mount pattern (internal/freeswitch/dynamic_gateway.go).
CREATE TABLE audio_assets (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,            -- operator-facing label
    fs_path    TEXT NOT NULL,            -- absolute path FreeSWITCH plays
    format     TEXT NOT NULL,            -- 'wav' | 'mp3'
    size_bytes BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

-- System-wide hold music for the tenant's queues: the fs_path of a chosen audio
-- asset, or '' to use the built-in default (local_stream://moh).
ALTER TABLE tenants ADD COLUMN hold_music TEXT NOT NULL DEFAULT '';

-- Per-tenant format of the finished call recording (the file extension).
-- ogg (Opus) is the smallest; mp3 plays anywhere; wav is uncompressed PCM.
ALTER TABLE tenants
    ADD COLUMN recording_format text NOT NULL DEFAULT 'ogg'
        CHECK (recording_format IN ('ogg', 'mp3', 'wav'));

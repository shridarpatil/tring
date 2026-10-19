-- Per-tenant sample rate (Hz) the finished call recording is encoded at. Only
-- the rates libopus accepts; 8000 (telephony narrowband) matches what a phone
-- call actually carries and keeps the file small.
ALTER TABLE tenants
    ADD COLUMN recording_sample_rate integer NOT NULL DEFAULT 8000
        CHECK (recording_sample_rate IN (8000, 12000, 16000, 24000, 48000));

-- Transport-neutral call columns + a call-source enum. The system now has two
-- media planes (the FreeSWITCH bridge + the pion plane in internal/media); fs_uuid
-- was already used as an opaque per-call leg id (internal/callcdr), so de-FS the
-- names. The two calls indexes on fs_uuid/fs_job_uuid ride the rename automatically.
ALTER TABLE calls RENAME COLUMN fs_uuid          TO leg_id;
ALTER TABLE calls RENAME COLUMN agent_fs_uuid    TO agent_leg_id;
ALTER TABLE calls RENAME COLUMN customer_fs_uuid TO customer_leg_id;
ALTER TABLE calls RENAME COLUMN fs_job_uuid      TO job_uuid;

-- RENAME COLUMN keeps the index objects' old auto-generated names; rename them
-- too so the schema reads consistently (cosmetic — the indexes already point at
-- the renamed columns).
ALTER INDEX calls_fs_uuid_idx     RENAME TO calls_leg_id_idx;
ALTER INDEX calls_fs_job_uuid_idx RENAME TO calls_job_uuid_idx;

ALTER TABLE audio_assets RENAME COLUMN fs_path TO file_path;

-- Where the call originated, so channel reporting is a direct GROUP BY instead of
-- string-matching to_number. 'whatsapp' is future-proofing (no writer yet).
CREATE TYPE call_source AS ENUM ('web', 'phone', 'whatsapp');

-- Add nullable first so existing rows don't violate NOT NULL, backfill with a
-- coarse heuristic on the old to_number='web' sentinel (alpha DB: historic
-- fidelity is low-stakes), then constrain + default.
ALTER TABLE calls ADD COLUMN source call_source;
UPDATE calls SET source = 'web', to_number = '' WHERE to_number = 'web';
UPDATE calls SET source = 'phone' WHERE source IS NULL;
ALTER TABLE calls ALTER COLUMN source SET DEFAULT 'phone';
ALTER TABLE calls ALTER COLUMN source SET NOT NULL;

-- S3 object key for a call's recording once it's uploaded off the local spool.
-- NULL = recording (if any) is still on the local volume; non-NULL = served
-- from S3. recording_url stays the API serve path regardless of backend.
ALTER TABLE calls ADD COLUMN recording_s3_key TEXT;

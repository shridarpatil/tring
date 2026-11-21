-- Per-tenant recording storage (was TRING_S3_* env, one bucket for all
-- tenants). Empty bucket = recordings stay on the local spool. Empty access
-- key + secret = the server's IAM role. s3_secret_key is sealed with
-- TRING_SECRETS_KEY (secretbox), like trunk SIP passwords.
ALTER TABLE tenants
    ADD COLUMN s3_bucket     TEXT    NOT NULL DEFAULT '',
    ADD COLUMN s3_region     TEXT    NOT NULL DEFAULT '',
    ADD COLUMN s3_endpoint   TEXT    NOT NULL DEFAULT '',
    ADD COLUMN s3_prefix     TEXT    NOT NULL DEFAULT 'recordings',
    ADD COLUMN s3_use_ssl    BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN s3_access_key TEXT    NOT NULL DEFAULT '',
    ADD COLUMN s3_secret_key TEXT    NOT NULL DEFAULT '';

-- Dev-only seed. Mounted by docker-compose into the Postgres
-- docker-entrypoint-initdb.d directory so it runs after schema.sql on a fresh
-- volume. Do NOT use this in production — the API key here is hardcoded and
-- public.

INSERT INTO tenants (name, api_key_hash, api_key_prefix, sip_domain) VALUES (
    'Tring Dev',
    encode(digest('tk_test_dev_local_only_change_me_in_prod', 'sha256'), 'hex'),
    'tk_test_dev_',
    't_dev.tring.local'
);

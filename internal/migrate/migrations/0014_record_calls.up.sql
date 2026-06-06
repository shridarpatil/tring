-- Per-tenant call recording toggle. Default true: connected calls are recorded
-- unless a tenant turns it off (consent/compliance is the tenant's call).
ALTER TABLE tenants ADD COLUMN record_calls BOOLEAN NOT NULL DEFAULT true;

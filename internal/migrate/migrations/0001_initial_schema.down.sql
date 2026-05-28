-- Reverse of 0001_initial_schema.up.sql. Drops the full v0.1 schema.
-- Order matters: tables with foreign keys must be dropped before the
-- tables they reference. CASCADE catches any indexes / constraints.

DROP TABLE IF EXISTS trunks CASCADE;
DROP TABLE IF EXISTS flows CASCADE;
DROP TABLE IF EXISTS webhook_deliveries CASCADE;
DROP TABLE IF EXISTS webhooks CASCADE;
DROP TABLE IF EXISTS masking_sessions CASCADE;
DROP TABLE IF EXISTS calls CASCADE;
DROP TABLE IF EXISTS numbers CASCADE;
DROP TABLE IF EXISTS agents CASCADE;
DROP TABLE IF EXISTS tenants CASCADE;

DROP TYPE IF EXISTS webhook_delivery_status;
DROP TYPE IF EXISTS call_status;
DROP TYPE IF EXISTS call_direction;
DROP TYPE IF EXISTS number_type;

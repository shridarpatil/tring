-- Drop the unused is_call_start flag from flows. Routing for inbound
-- calls is purely numbers.flow_id; this column was never consulted by
-- the executor. Down-migration restores it as nullable-default-false in
-- case a future feature wants the field back.

ALTER TABLE flows DROP COLUMN IF EXISTS is_call_start;

-- The customer/visitor FreeSWITCH leg — the party that should hear hold music.
-- Hold plays MOH into THIS leg via media injection (uuid_displace), not
-- uuid_hold (which would re-INVITE a media-gateway leg and drop the call).
-- NULL for inbound, where fs_uuid already IS the customer (visitor) leg; set for
-- outbound click-to-call, where fs_uuid is the agent leg and the customer is the
-- bridged B-leg. Control uses COALESCE(customer_fs_uuid, fs_uuid).
ALTER TABLE calls ADD COLUMN customer_fs_uuid TEXT;

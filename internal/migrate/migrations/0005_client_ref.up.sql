-- Authenticated web callers: when the embedding app (e.g. a logged-in support
-- portal) already knows who the caller is, it can attach a client reference
-- when minting guest credentials. We carry it from the guest session onto the
-- call so the call log shows the client instead of an anonymous guest_* id.
ALTER TABLE guest_sessions ADD COLUMN client_ref TEXT;
ALTER TABLE calls          ADD COLUMN client_ref TEXT;

-- How long an agent's transfer rings the target before the call goes back to
-- the transferring agent (who waits, muted and deafened, meanwhile).
ALTER TABLE tenants
    ADD COLUMN transfer_timeout_seconds INT NOT NULL DEFAULT 60
        CHECK (transfer_timeout_seconds BETWEEN 5 AND 600);

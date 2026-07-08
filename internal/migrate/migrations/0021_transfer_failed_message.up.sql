ALTER TABLE tenants
    ADD COLUMN transfer_failed_message text NOT NULL
        DEFAULT 'Sorry, no one is available to take your call right now. Please try again later.',
    ADD COLUMN transfer_failed_audio text;

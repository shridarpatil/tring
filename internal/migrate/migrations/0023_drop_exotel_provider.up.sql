-- The exotel provider was removed. Its trunks would now fall back to the
-- generic (pass-through) provider anyway; store that explicitly so the console
-- and API don't show a provider type the code no longer knows.
UPDATE trunks SET provider_type = 'generic' WHERE provider_type = 'exotel';

-- Drop the ephemeral guest SIP-session table. Since the M5 cutover (ADR-001)
-- visitors place web calls straight at the media gateway with a short-lived
-- gateway call token (a JWT carrying the pinned flow + optional client_ref) —
-- there's no minted SIP identity to persist. The token is stateless, so the
-- table has no remaining purpose. The trusted-mint's client_ref now rides the
-- token to the gateway and onto the call's CDR (calls.client_ref).
DROP TABLE IF EXISTS guest_sessions;

-- Agents no longer SIP-register to FreeSWITCH (they reach calls over WebRTC
-- through the media gateway), so the per-tenant SIP domain and the agents'
-- generated SIP passwords have no reader. sip_username stays: it is the
-- agent's identity everywhere (presence, routing, transfer, token minting).
ALTER TABLE tenants DROP COLUMN sip_domain;
ALTER TABLE users DROP COLUMN sip_password;

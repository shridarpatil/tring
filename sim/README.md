# SIP simulator (local Tata trunk stand-in)

A tiny SIPp UAS that pretends to be the carrier's SBC — listens on UDP/5070, answers every INVITE with `100 Trying` → `180 Ringing` → `200 OK`, waits for BYE. Lets you verify the full Tring → trunk signaling path before the real Tata circuit lands.

## What it tests
- ✅ Trunk gateway XML is rendered correctly from the DB
- ✅ FS picks up gateways on sofia rescan
- ✅ Outbound INVITE leaves FS with the right Via/Contact/SDP
- ✅ Tring's call state machine processes 100/180/200 correctly
- ✅ Call rows transition `ringing` → `answered` → `ended`
- ✅ BG-job UUID parsing, CDR settlement, hangup-cause capture
- ✅ Webhook events (`call.ringing`, `call.answered`, `call.ended`) fire

## What it does NOT test
- ❌ Real PSTN connectivity (no actual phone rings)
- ❌ Audio quality / codec negotiation under load (SIPp sends no RTP)
- ❌ Tata's specific SBC quirks (response codes, header munging)
- ❌ DTMF on the live network

## Quick start

```bash
docker compose up -d sip-sim
docker compose logs -f sip-sim
```

You should see SIPp announce it's listening on UDP/5070.

Create a "Test Tata" trunk in the admin console (`/trunks`):
- **Name**: `Local Sim`
- **REGISTER auth**: off (IP-auth mode)
- **SBC proxy IP**: `127.0.0.1:5070`
- **Active**: on

Then originate a test call:
```bash
KEY='tk_test_dev_local_only_change_me_in_prod'
curl -X POST http://localhost:8085/v1/calls/connect \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"agent_number":"+919876543210","client_number":"+919812345678"}'
```

Watch `docker compose logs -f sip-sim` — you should see `received INVITE` and the round-trip complete. Check `GET /v1/calls` for the call row state.

## What "success" looks like

In SIPp logs:
```
[sim] received INVITE from 192.168.x.x:xxxx
[sim] call established, waiting for BYE
[sim] received BYE — call duration logged in stats
```

In Tring's API:
```json
{"id":"…","status":"ended","hangup_cause":"NORMAL_CLEARING", …}
```

If you see `MEDIA_TIMEOUT` instead, that's expected — SIPp doesn't send RTP. To suppress it, set `rtp-timeout-sec=0` on the trunk via the `extra_params` field (Phase 2 feature; for now ignore the cause, signaling is what matters).

## When to retire this

The moment a real Tata trunk is up and you can place a real PSTN call, this sim is obsolete. Switch the trunk's `proxy_ip` from `127.0.0.1:5070` to the real Tata SBC IP and you're on the real circuit — no other code change.

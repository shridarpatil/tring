# Day 9 — Going live on a real Indian trunk

Day 9 in the Phase-1 plan: take Tring from dev-loopback to a real Indian PSTN trunk. **No Go code changes** at this point — the work is procurement (Exotel / Airtel / Jio / Tata) and configuration on the Tring side.

Tring ships four Indian trunks pre-baked: **Exotel**, **Airtel**, **Jio**, and **Tata**. They share identical wiring (E.164 normalization, gateway XML shape, env var schema) — the only differences are the credentials you provide and the value of `numbers.provider` when you register a DID. Switching from one to another is a single API call:

```bash
# Was on Exotel, want to move to Airtel? Just register the new DID.
curl -X POST -H "Authorization: Bearer $KEY" \
     -H 'Content-Type: application/json' \
     -d '{"e164":"+918001234567","type":"outbound","provider":"airtel","provider_did_id":"<id>"}' \
     http://localhost:8085/v1/numbers
# The next outbound call routes via Airtel and stamps the new DID as caller-ID.
```

No FreeSWITCH restart, no Tring restart, no code change.

## Pre-reqs

- Exotel account with a **SIP trunk** product enabled (not just the REST-only one). Talk to Exotel sales if the dashboard shows only "Apps" and no "SIP trunk".
- At least one **outbound DID** allocated to your trunk. Exotel may issue a virtual number tied to the trunk; if your use-case calls for the **1600** transactional series, request that explicitly during onboarding.
- DLT / use-case approval (often a sales-engineer call). "Click-to-call from CRM, B2C outbound, customer consent flow" is a recognized pattern.

## Day-of go-live checklist (works for any of the four providers)

Use `EXOTEL` / `AIRTEL` / `JIO` / `TATA` as the prefix for whichever provider you're activating.

1. **Get credentials** from your trunk provider: SIP username, password, and realm. The realm varies:
   - Exotel: `<account>.pstn.exotel.com` (or `.in.exotel.com`)
   - Airtel: assigned by your Airtel account manager
   - Jio: `sip.jio.com` or similar (varies by enterprise contract)
   - Tata: `sip.tatateleservices.com` (varies)
2. **Drop them into `.env`** at the repo root (gitignored):
   ```bash
   cp .env.example .env
   $EDITOR .env
   # Set <PROV>_SIP_USERNAME, _PASSWORD, _REALM
   # Set <PROV>_REGISTER=true
   ```
3. **Recreate FreeSWITCH** to pick up the new env:
   ```bash
   docker compose up -d --force-recreate freeswitch
   ```
4. **Verify the gateway registered:**
   ```bash
   docker exec tring-fs fs_cli -x "sofia status profile external"
   ```
   You should see a row for your provider with state `REGED` within ~30s. If it stays `NOREG` or shows an error state, check `docker logs tring-fs | grep -i <provider>` — common causes are wrong realm, wrong password, or your trunk IP not allowlisted on the provider's side.
5. **Register your outbound DID with Tring** so it gets stamped as caller-ID:
   ```bash
   curl -X POST -H "Authorization: Bearer $KEY" \
        -H 'Content-Type: application/json' \
        -d '{"e164":"+91XXXXXXXXXX","type":"outbound","provider":"exotel","provider_did_id":"<their-id>"}' \
        http://localhost:8085/v1/numbers
   ```
   Replace `"exotel"` with `"airtel"`, `"jio"`, or `"tata"` as appropriate. For 1600 numbers (Exotel offers these), the e164 is just `+911600XXXXXX` — same shape, same flow.
6. **Place a real test call:**
   ```bash
   curl -X POST -H "Authorization: Bearer $KEY" \
        -H 'Content-Type: application/json' \
        -d '{"agent_id":"<id>","to":"+919876543210"}' \
        http://localhost:8085/v1/calls
   ```
   Your registered WebRTC agent rings first; on answer, the customer's mobile rings with your Exotel DID as the caller-ID.

## What's enforced in code (so credentials are the only variable on Day 9)

- **E.164 normalization** at `internal/dial/normalize.go` — accepts `+91...`, `91...`, `0...`, and bare 10-digit Indian mobile inputs; rejects non-Indian numbers and landlines. The Exotel provider only accepts numbers that survive this; the API returns `400 invalid destination` for anything else.
- **Caller-ID** is the tenant's oldest `numbers WHERE type='outbound'` row's `e164`, stamped via the `origination_caller_id_number` channel variable on every originate. There's no way to spoof a different caller-ID via the API — only what's been registered with us via `POST /v1/numbers` is usable.
- **Inert when uncredentialed**: `EXOTEL_REGISTER=false` (the default) means FreeSWITCH parses the gateway XML but never tries to register. No log spam on dev boxes.

## Common Day-9 surprises

| What you see | What's actually wrong |
|---|---|
| `sofia status` shows `exotel` REGED but originates fail with `USER_BUSY` immediately | Exotel's side may not have your trunk IP allowlisted for outbound. Their dashboard usually has a "Source IPs" list to fill. |
| Originate fails with `INVALID_NUMBER_FORMAT` | The destination you sent didn't normalize. Test it manually: `go test ./internal/dial -run India -v`. |
| Calls connect but customer hears silence | RTP NAT problem. The safarov/freeswitch image uses `network_mode: host` which is fine on a Linux host with a public IP. If you're behind NAT, set `external_rtp_ip` in `vars.xml` to your public IP (or a STUN lookup). |
| Caller-ID shows your trunk username, not your DID | `caller-id-in-from=true` is set, but Exotel may still rewrite based on their own rules. Confirm with Exotel support that the DID you registered is allowed as a CLI for outbound. |
| Outbound rejected with `UNAUTHORIZED` cause | Trunk is registered but Exotel hasn't approved your use case yet, or the requested DID isn't allocated to this trunk. |

## What this *doesn't* do (and shouldn't, in Phase 1)

- Per-tenant trunk credentials. Today it's one Exotel account per Tring deployment. Phase 2 templates the gateway XML per tenant from Postgres.
- Per-tenant allowed dialing prefixes (the `01-23-45` "only call these area codes" rule). The Exotel provider enforces "Indian mobile only" — that's the v0 line. Phase 2 adds finer-grained per-tenant policy.
- Rate limiting / fraud detection. A compromised API key could rack up real money in the trunk's per-minute charges. Phase 2 adds per-tenant call rate caps + suspicious-pattern detection.

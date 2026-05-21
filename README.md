# Tring

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)](#status)

**Open-source, API-first voice calling for any CRM.** Drop a `<script>` tag and a `<button>` into your app — your agents make and take real phone calls in minutes, not weeks. Multi-tenant. India-primary. Self-host or hosted, same binaries.

> v0.1 (working call button demo) is the current milestone. See [docs/PHASE_1_PLAN.md](docs/PHASE_1_PLAN.md) for the 10-day plan that got us here. Phase 2 work tracked in CLAUDE.md.

## Why Tring

The Indian voice-CPaaS market is dominated by closed-source platforms (Exotel, Knowlarity, MyOperator) that bundle the orchestration layer with their trunks. Tring decouples the two: **bring your own trunk** — Exotel, Airtel, Jio, Tata, or any raw SIP carrier — and Tring handles the API, browser SDK, IVR, recording, CDR webhooks, and TRAI/DLT-aware routing.

| | Twilio Voice | Exotel | Tring |
|---|---|---|---|
| Source available | No | No | **Yes (Apache 2.0)** |
| Self-host | No | No | **Yes** |
| India trunk swap (Airtel ↔ Jio ↔ Tata) | n/a | locked-in | **Config-only, no code change** |
| 1600 transactional series | n/a | Yes | Yes (via Exotel) |
| Browser SDK | Yes | Limited | Yes (sip.js wrapper) |
| Per-tenant trunks | Yes | n/a | Planned (Phase 2) |

## Quickstart (one command, fully containerized)

```bash
git clone https://github.com/shridarpatil/tring && cd tring
docker compose up -d                  # All four services: API + orchestrator + FreeSWITCH + Postgres
```

That brings up:
- `tring-api` — REST API on `:8085`
- `tring-orchestrator` — ESL event subscriber (writes CDRs, fires webhooks)
- `tring-fs` — FreeSWITCH (`:5060` SIP, `:5066` plain WS, `:7443` WSS, `:8021` ESL)
- `tring-db` — Postgres on `:5434` (moved off `:5432` to coexist with other local DBs)

`docker compose logs -f` to tail everything. `docker compose down` to stop.

For local dev with hot-reload on the Go services, `docker compose stop api orchestrator` then `make api` / `make orchestrator` (host `go run`).

The dev seed creates a tenant with API key `tk_test_dev_local_only_change_me_in_prod`. Place your first call (loopback echo, no real trunk needed):

```bash
KEY='tk_test_dev_local_only_change_me_in_prod'
AGENT=$(curl -sS -H "Authorization: Bearer $KEY" http://localhost:8085/v1/agents | jq -r '.[0].id // empty')
# If no agent exists yet, create one:
[ -z "$AGENT" ] && AGENT=$(curl -sS -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"sip_username":"alice","display_name":"Alice"}' \
  http://localhost:8085/v1/agents | jq -r .id)

curl -sS -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d "{\"agent_id\":\"$AGENT\",\"to\":\"9196\"}" \
  http://localhost:8085/v1/calls | jq
```

You'll get a `Call` row back with `status=ringing`. Once the agent's browser registers (see SDK below), the call actually rings.

### Connect two phone numbers (click-to-call / callback)

When the agent is on a **phone** rather than a browser, `POST /v1/calls/connect` rings the agent's number first and, on answer, bridges the client — both legs over the tenant's trunk, both presenting the trunk's DID as caller ID. No softphone required on either end.

```bash
curl -sS -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"agent_number":"+919876543210","client_number":"+919812345678"}' \
  http://localhost:8085/v1/calls/connect | jq
```

This is the Exotel-style flow: your CRM hits this endpoint, the agent's phone rings, they answer, the customer is dialed and bridged. Needs a live outbound trunk (in dev with no trunk it falls back to the loopback path).

## Drop-in browser widget

```html
<!-- Anywhere in your CRM template -->
<button data-tring-call data-to="+919876543210">Call customer</button>

<!-- Load once, near </body> -->
<script src="https://cdn.tring.io/voice.js"
        data-api-base="https://api.tring.io"
        data-token-url="/tring/token"></script>
```

`/tring/token` is **your** server-side endpoint that mints a fresh 5-min JWT for the logged-in agent using your tenant API key. The browser never holds the API key.

See [`examples/sample-crm.html`](examples/sample-crm.html) for a runnable end-to-end demo, and [`sdk/js/README.md`](sdk/js/README.md) for the full SDK reference.

## Provider swap (Exotel → Airtel → Jio → Tata)

Four Indian trunks ship pre-baked. Switching is **one API call**, no code edits:

```bash
curl -X POST -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
     -d '{"e164":"+918001234567","type":"outbound","provider":"airtel","provider_did_id":"<id>"}' \
     http://localhost:8085/v1/numbers
```

The next outbound call routes through the new trunk and stamps the new DID as caller-ID. See [`docs/PHASE_1_DAY_9_EXOTEL.md`](docs/PHASE_1_DAY_9_EXOTEL.md) for procurement and `<PROV>_REGISTER=true` activation.

## Architecture

```
Browser (CRM)  ──WSS:7443──►  FreeSWITCH  ──SIP──►  Trunk  ──PSTN──►  Customer
       │                          ▲
       │                          │ ESL
       ▼                          ▼
   Tring API (:8085)  ◄────►  Orchestrator (:8021)  ◄────►  Postgres (:5434)
       (call origination,        (channel events,
        webhook fan-out)          CDR updates,
                                  webhook fan-out)
```

Two Go services share one Postgres. The API is stateless and horizontally scalable. The orchestrator holds a persistent ESL connection and processes FreeSWITCH events into CDR rows + webhook deliveries.

| Path | What it is |
|------|-----------|
| `cmd/api/` | Public REST + WSS API. Tenant/agent/number CRUD, JWT minting, originate, webhook CRUD. |
| `cmd/orchestrator/` | FreeSWITCH-facing ESL subscriber. Settles calls, emits answered/ended webhooks. |
| `internal/api/` | HTTP handlers, auth (API key + JWT + Basic), the public surface. |
| `internal/freeswitch/` | ESL wrapper (originate, …). |
| `internal/provider/` | Trunk abstraction. Exotel, Airtel, Jio, Tata, Loopback. |
| `internal/webhook/` | Notifier — signs, delivers, retries with backoff. |
| `internal/orchestrator/` | Event loop + CDR settlement. |
| `internal/dial/` | E.164 normalization (India-aware). |
| `sdk/js/` | Browser SDK + drop-in `voice.js` widget. |
| `freeswitch/conf/` | Dialplan, sip_profiles, autoload_configs. |
| `db/` | Schema + dev seed. |
| `examples/` | Demo CRM page. |
| `docs/` | Architecture, Day-9 trunk go-live, demo script. |

See [CLAUDE.md](CLAUDE.md) for the deeper architectural map, auth model, eslgo gotchas, FreeSWITCH config landmines, and Phase 2 roadmap.

## What's in v0.1

| | |
|---|---|
| **REST API** | tenants, agents, numbers, calls, webhooks. API key auth + JWT auth. 20+ endpoints. |
| **Originate** | `POST /v1/calls` and `POST /v1/sdk/calls`. Pre-allocated channel UUID, bgapi result captured for failed-originate handling. |
| **CDR settlement** | Orchestrator subscribes to FreeSWITCH events. `CHANNEL_ANSWER`, `CHANNEL_HANGUP_COMPLETE`, and `BACKGROUND_JOB` (for originates that fail before a channel exists) all flow back into `calls` rows. |
| **Webhooks** | `call.ringing`, `call.answered`, `call.ended`. HMAC-SHA256 signed (Stripe pattern). 6 attempts with 1+2+4+8+16s backoff. Delivery log with `last_error`. |
| **Browser SDK** | Tring class + sip.js. Outbound calls auto-bridge inbound INVITE for agent leg. DTMF, hangup, event emitter. ~500KB unminified (incl. sip.js). |
| **Drop-in widget** | `voice.js` — `<button data-tring-call data-to="…">` auto-bound on DOM ready. Floating call panel. `MutationObserver` rebinds dynamically-rendered rows. |
| **Trunk providers** | Exotel, Airtel, Jio, Tata pre-baked. Loopback for dev. India E.164 normalization + validation. Config-only swap. |
| **Multi-tenant** | Per-tenant SIP domain, API keys (SHA-256 hashed + 12-char display prefix), tenant scoping on every query, `mod_xml_curl` dynamic directory. |

## What's *not* in v0.1 (Phase 2 work)

- IVR builder (Vue UI port from the upstream Whatomate Frappe app).
- Call recording.
- Inbound masked-DID flows (the masking pool exists but isn't exposed via API yet).
- Multi-region FreeSWITCH clustering.
- Billing / per-minute rating.
- DLT/TRAI consent list enforcement, time-of-day gating.
- Admin UI.

## License

[Apache-2.0](LICENSE) — both `tring/` (orchestration) and `sdk/js/` (browser SDK).

## Status

Pre-alpha. v0.1 works end-to-end locally and is suitable for proof-of-concept. Production hardening (TLS termination, secret management, multi-region) is Phase 2.

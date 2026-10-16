# Tring

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)](#status)

**Open-source, API-first voice calling for any CRM.** Drop a `<script>` tag and a `<button>` into your app — your agents make and take real phone calls in minutes, not weeks. Multi-tenant. India-primary. Self-host or hosted, same binaries.

> The system has converged on **one pion media plane** with FreeSWITCH demoted to a pluggable PSTN trunk edge. [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) is the architectural source of truth; [`docs/STATE_AND_HA.md`](docs/STATE_AND_HA.md) covers what survives a restart and how the plane scales.

## Why Tring

The Indian voice-CPaaS market is dominated by closed-source platforms (Exotel, Knowlarity, MyOperator) that bundle the orchestration layer with their trunks. Tring decouples the two: **bring your own trunk** — Exotel, Airtel, Jio, Tata, or any raw SIP carrier — and Tring handles the API, browser SDK, IVR, recording, CDR webhooks, and TRAI/DLT-aware routing.

| | Twilio Voice | Exotel | Tring |
|---|---|---|---|
| Source available | No | No | **Yes (Apache 2.0)** |
| Self-host | No | No | **Yes** |
| India trunk swap (Airtel ↔ Jio ↔ Tata) | n/a | locked-in | **Config-only, no code change** |
| 1600 transactional series | n/a | Yes | Yes — via any licensed carrier (Tata, Airtel, Jio) |
| Browser SDK | Yes | Limited | Yes (raw WebRTC, **no SIP in the browser**) |
| Visual IVR builder | Limited | Yes | **Yes (node-graph editor + live simulator)** |
| Per-tenant trunks | Yes | n/a | Planned (Phase 2) |

## Quickstart (no clone)

```bash
curl -fsSL https://raw.githubusercontent.com/shridarpatil/tring/main/install.sh | sh
cd tring
cp .env.example .env                  # edit IPs, secrets, admin password (see below)
docker compose pull && docker compose up -d
```

The install script drops `docker-compose.yml` and `.env.example` into a `tring/` directory (override with `TRING_DIR=/srv/tring`). The FreeSWITCH config is baked into the `tring-freeswitch` image — nothing to bind-mount.

That brings up:
- `tring-api` — REST API + admin console + served SDK bundles (`:8085`, console at `/admin/`)
- `tring-gateway` — **pion media plane + in-process brain** (`:8087`); browsers (visitors *and* agents) peer here over raw WebRTC on `/rtc`. Runs the IVR, agent routing, recording, and CDR. **No FreeSWITCH in web calls.**
- `tring-fs` — FreeSWITCH (`:5060` SIP, `:8021` ESL) — **private**, the **outbound PSTN trunk edge only**, never internet-exposed
- `tring-db` — Postgres on `:5434` (moved off `:5432` to coexist with other local DBs)
- `tring-redis` — agent presence + single-use call-token store (`:6379`). **Hard dependency for the gateway** when `TRING_GATEWAY_REQUIRE_AUTH=true` (the default) — the gateway refuses to boot without it.

`docker compose logs -f` to tail everything. `docker compose down` to stop.

The first `docker compose up` auto-seeds a tenant + an admin console user from `TRING_BOOTSTRAP_TENANT` / `TRING_ADMIN_EMAIL` / `TRING_ADMIN_PASSWORD` in `.env` (defaults: `admin@tring.local` / `tring_dev_change_me`). Open `http://localhost:8085/admin/` and sign in.

For a real server (not localhost) the network IPs are what people get wrong — see section 5 of `.env.example`:

```bash
TRING_GATEWAY_PUBLIC_URL=https://gw.your-domain   # browser-reachable, must hit /rtc
TRING_PUBLIC_IP=<public/elastic ip>               # rewrites the gateway's ICE candidate
TRING_REDIS_ADDR=127.0.0.1:6379                   # gateway hard-dep when auth is on
```

### Hack on the source

```bash
git clone https://github.com/shridarpatil/tring && cd tring
docker compose up -d
```

For hot-reload on the Go services, run them on the host against the same Postgres/FreeSWITCH/Redis:

```bash
make api        # cmd/api      — REST API + admin console on :8085
make gateway    # cmd/gateway  — pion media plane + brain on :8087
```

Run `make help` for the full target list (`make sdk`, `make images`, `make test`, …). After changing Go code **or `freeswitch/conf/*`**, `make images` rebuilds the container images so `docker compose up` picks up the changes.

## Drop-in browser widget

The agent widget is a **WebSocket-only media client** — it rings/answers calls but **cannot place them** (no Tring API access). Drop one tag in your agent app:

```html
<!-- Load once, near </body> -->
<script src="https://api.tring.io/sdk/agent.js"
        data-token-url="/tring/token"></script>
```

`/tring/token` is **your** server-side endpoint that mints a fresh 5-min agent JWT (`GET /v1/agents/{id}/token`) with your tenant API key and passes its response through — `{ token, gateway_url, ice_servers }`. The browser never holds the API key, and connects to the gateway over **raw WebRTC** (the pion SFU) — no SIP, ever.

To **place** an outbound call, your own button hits **your** backend, which originates via the Tring API — so the agent never sees the dialed number:

```bash
curl -X POST -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
     -d '{"agent_id":"<id>","to":"+919876543210","agent_label":"Ticket #4821"}' \
     https://api.tring.io/v1/calls
```

Add `"caller_id":"<one of your numbers>"` to choose the number the customer sees (e.g. your support number for a masked callback); it must be one of the tenant's active numbers.

Visitor "call us" calling uses `voice.js` the same way — a `data-credentials-url` that mints a short-lived, **single-use** gateway call token server-side (`POST /v1/guest-credentials`), pinning the IVR flow server-side.

See [`examples/demo-portal/`](examples/demo-portal/) for a runnable end-to-end demo (anonymous "call us", logged-in support, and the agent CRM in one app), and [`sdk/js/README.md`](sdk/js/README.md) for the full SDK reference.

## Provider swap (Airtel → Jio → Tata)

Four Indian trunks ship pre-baked. Switching is **one API call**, no code edits:

```bash
curl -X POST -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
     -d '{"e164":"+918001234567","type":"outbound","provider":"airtel"}' \
     http://localhost:8085/v1/numbers
```

The next outbound call routes through the new trunk and stamps the new DID as caller-ID. See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for procurement and `<PROV>_REGISTER=true` activation.

## WhatsApp calls (inbound)

WhatsApp users can call your WhatsApp Business number and land in the same IVR flows and agent queues as phone calls, via Meta's [SIP calling](https://developers.facebook.com/docs/whatsapp/cloud-api/calling/sip). FreeSWITCH runs a separate, TLS-only `whatsapp` SIP profile next to the carrier trunk.

1. **Hostname + cert.** Point a hostname (e.g. `wa-sip.example.com`) at the server's public IP and get a public-CA certificate for it. Put the cert chain + private key in `freeswitch/tls/whatsapp/agent.pem` (`cat fullchain.pem privkey.pem > agent.pem`) and Meta's outbound API CA certificate in `freeswitch/tls/whatsapp/cafile.pem`. Only connections presenting Meta's client certificate (`client.sip.fbclientcerts.com`) are accepted.
2. **Enable** in `.env`: `TRING_WHATSAPP_ENABLED=true`, plus `TRING_WHATSAPP_IP` / `TRING_WHATSAPP_PUBLIC_IP` / `TRING_WHATSAPP_TLS_PORT` if the defaults don't fit (see `.env.example`). Recreate FreeSWITCH.
3. **Firewall.** Open the TLS port (default 5061/tcp) and the RTP range only to Meta's published IP ranges.
4. **Meta side** (`POST /<PHONE_NUMBER_ID>/settings`): enable SIP with your hostname and port, and set `"srtp_key_exchange_protocol": "SDES"`.
5. **Number.** Add the WhatsApp number as an inbound number with its flow, written with the leading `+` (e.g. `+9122XXXXXXXX`) — Meta dials `sip:+<E.164>@<host>` and lookup is an exact match.

After renewing the certificate, run `fs_cli -x "sofia profile whatsapp restart"` (the profile takes ~30 s to come back). Outbound (business-initiated) WhatsApp calls are not supported yet. A caller without a visible phone number may appear in `{{caller}}` as a WhatsApp user ID rather than a number.

## Architecture

```
                      ┌──────────────────── cmd/gateway (:8087) ─────────────────────┐
 Browser (visitor) ─raw WebRTC /rtc─►  pion SFU ─┐                                    │
 Browser (agent)   ─raw WebRTC /rtc─►  one room  ┼─► brain: IVR + agent routing + CDR │
                                                 └─► recording (stereo Ogg) ─► spool/S3│
                      └──────────────────────────────────────────────────────────────┘
                            ▲   shared Postgres (:5434) + Redis (:6379)   │
                            │                                             ▼
   CRM backend ──REST──► cmd/api (:8085) ──ESL originate──► FreeSWITCH ──SIP──► Trunk ──► PSTN
                         REST + admin console + /sdk + webhooks   (outbound PSTN edge, private)
```

**Two Go binaries, one Postgres, one Redis, one private FreeSWITCH.** Every call — browser today, phone/WhatsApp later — converges on one pion media plane: a call is a `room` with N participants, so a visitor, the IVR, and an agent are all just participants, and "route to a human" is a membership change, not a re-bridge. The **brain** (IVR + agent routing + CDR) runs **in-process** in the gateway — there is no separate orchestrator service. FreeSWITCH is a swappable PSTN plug behind a Go interface, reached by the API over ESL for **outbound** originate. See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

| Path | What it is |
|------|-----------|
| `cmd/api/` | Public REST API + admin console + served SDK bundles. Tenant/agent/number/flow CRUD, JWT minting, originate over ESL, webhook CRUD. Stateless, horizontally scalable. |
| `cmd/gateway/` | The call plane: pion media plane (`/rtc`) + the in-process brain. State-aware; scale by adding instances. |
| `internal/media/` | The pion media plane — `room` (transport-agnostic core + the `Plug` PSTN seam), `sfu` (the only pion importer), `signaling` (the `/rtc` WebSocket), `ivrdrive`, `prompt`, `record`. |
| `internal/brain/` | Transport-neutral IVR session runner + agent router, embedded in the gateway. |
| `internal/ivr/` | Transport-agnostic IVR flow model + walker (drives the runtime and the simulator). |
| `internal/routing/` | Shared agent queue/ring engine (strategies, reservations, presence filter, queue depth). |
| `internal/callcdr/` | Records media-plane calls into the same `calls` table / recording pipeline the PSTN path uses. |
| `internal/provider/` | Trunk abstraction. Exotel, Airtel, Jio, Tata, Loopback. |
| `internal/freeswitch/` | ESL wrapper for outbound originate. |
| `internal/webhook/` | Notifier — signs, delivers, retries with backoff. |
| `internal/tts/` | Piper + ffmpeg → cached Opus-Ogg synthesis of IVR prompt text at flow-save. |
| `internal/dial/` | E.164 normalization (India-aware). |
| `sdk/js/` | Browser SDK + drop-in `voice.js` (visitor) / `agent.js` (agent) widgets. |
| `web/` | Admin console (Vue 3), embedded into the API binary. |
| `freeswitch/conf/` | Dialplan, sip_profiles, autoload_configs (baked into the image). |
| `internal/migrate/migrations/` | Postgres schema (source of truth; applied on API startup). |
| `examples/` | Runnable demos (`demo-portal/`, sample CRM pages). |

See [CLAUDE.md](CLAUDE.md) for the deeper architectural map, auth model, eslgo gotchas, FreeSWITCH config landmines, and roadmap.

## What's working

| | |
|---|---|
| **Web calling** | Visitors and agents peer with the gateway over raw WebRTC on `/rtc` (pion SFU). No SIP, no FreeSWITCH in the path. Single-use, flow-pinned call tokens; page-refresh grace window. |
| **REST API** | Tenants, agents, numbers, flows, calls, webhooks. API key + JWT + console auth. |
| **Visual IVR** | Node-graph builder in the console (greeting/menu/gather/http_callback/transfer/goto/timing/hangup), a runtime driver on the pion plane, and a wall-clock-free **simulator** powering a live Test panel. Prompt text auto-synthesized to audio at flow-save (Piper). |
| **Agent routing** | Shared queue/ring engine: `ring_all` / `round_robin` / `sequential` / `longest_wait`, Redis-backed presence + reservations, live queue depth (phone + web summed). |
| **Call control** | Hold / resume / blind transfer (to an agent or a team) from the agent SDK or the CRM backend. |
| **Recording** | Opt-in per tenant (`record_calls`). Stereo (agent **left**, customer **right**), captured on the media plane and merged on hangup. Starts at agent answer. Served by the API; optional S3 upload. |
| **CDR + webhooks** | One `calls` table for every source. `call.ringing` (API on originate), `call.answered` / `call.ended` (gateway on media events). HMAC-SHA256 signed (Stripe pattern), up to 6 attempts with backoff, delivery log. |
| **Trunk providers** | Exotel, Airtel, Jio, Tata pre-baked + Loopback for dev. Multi-trunk failover / round-robin / manual strategies. Config-only swap. |
| **Browser SDK** | `voice.js` (visitor), `agent.js` (agent), bare `tring.js` + ESM. Served by the API at `/sdk/*` — no CDN needed. |
| **Multi-tenant** | Per-tenant SIP domain, API keys (SHA-256 hashed), tenant scoping on every query, `mod_xml_curl` dynamic directory, admin console. |

## Not yet (roadmap)

- **Inbound over FreeSWITCH** — the outbound trunk path is live; an inbound FreeSWITCH plug behind the `room.Plug` seam (and outbound-trunk CDR settlement) returns with the first live SIP trunk.
- Inbound masked-DID flows (the masking pool exists but isn't exposed via API yet).
- Per-tenant trunks, multi-region clustering, live-call HA migration.
- Billing / per-minute rating; DLT/TRAI consent-list enforcement.
- Attended transfer + conference; AI voice agent (plugs in as another media leg).

## License

[Apache-2.0](LICENSE) — both the orchestration services and `sdk/js/` (browser SDK).

## Status

Alpha. Works end-to-end locally and on a single server; suitable for proof-of-concept and pilot. Production hardening (multi-region, live-call HA, billing) is ongoing — see [`docs/STATE_AND_HA.md`](docs/STATE_AND_HA.md).

---
title: "Architecture"
description: "How Tring is built: one pion media plane, the in-process brain, and FreeSWITCH as the PSTN edge."
---

The single source of architectural truth for Tring. For code conventions see
[Code style](/tring/contributing/code-style/); for how to run the repo see the
[README](https://github.com/shridarpatil/tring#readme).

---

## 1. What Tring is

An open-source, API-first voice CPaaS — "OSS Twilio Voice, India-primary." A CRM
developer drops a `<script>` + a `<button>` into their app and their agents place
and receive real phone calls. Tring orchestrates the media; the integrator never
sees SIP or FreeSWITCH.

- **License** Apache 2.0 · **Tenancy** multi-tenant from day one · **Delivery**
  hosted SaaS *and* self-host, same binaries · **Geography** India-primary
  (Airtel/Jio/Tata trunks), international via raw-SIP.

## 2. The big idea — one brain, one media plane, pluggable call sources

Every call — browser, phone, (future) WhatsApp — converges on **one media anchor
(pion) driven by one brain**. A call *source* is just an adapter that feeds a leg
into a room; from there the call is identical regardless of how it arrived: same
IVR, same transfer-to-agent, same recording, same CDR.

```
   web /rtc ┐                                   ┌ recording (stereo Ogg)
   phone FS ┼─▶  ROOM (pion SFU)  ──IVR/transfer─┤ CDR
   whatsapp ┘     one media plane                └ agents (web /rtc)
            ▲                                          ▲
       call SOURCE adapters                   served the same way
```

This is the **convergence** (the project's defining decision): no dual prompt
formats, no dual maintenance. FreeSWITCH is demoted to *one swappable PSTN plug*
behind a Go interface — replaceable by sipgo or any other stack without touching
the call logic. A future AI voice agent plugs in as just another media leg
(`ParticipantSource` for TTS in, `TrackSink` for STT out).

## 3. Processes & datastores

Two Go binaries, one Postgres, one Redis, one (PRIVATE) FreeSWITCH:

| Binary | Role | Faces | State | Ports |
|---|---|---|---|---|
| **`cmd/api`** | Public REST + admin console + token minting; outbound PSTN originate via FS ESL | integrators (server→server), operators | stateless | `8085` |
| **`cmd/gateway`** | The call plane: browser-facing **pion media plane** (`/rtc`) with the **brain** (IVR + agent routing + CDR) embedded **in-process** | browsers (WebRTC) | in-proc rooms | `8087` (+ `/rtc`) |

Datastores: **Postgres** (the system of record — see §10), **Redis** (agent
presence + single-use token jtis + cross-plane queue depth/reservations; a hard
dependency when gateway auth is on), **FreeSWITCH** (PSTN trunk edge only —
terminates outbound trunks, transcodes G.711↔Opus; never internet-exposed; not
involved in web calls at all).

> The brain is **not** a separate process — it's a library (`internal/brain`)
> the gateway calls in-process. There is no orchestrator and no brain↔edge
> control channel. The api/gateway split remains only because they scale
> differently (stateless REST vs. stateful media plane).

## 4. The media plane (`internal/media`) — the product core

A pion `webrtc/v4` SFU. The server is the **sole offerer**: it sends SDP offers,
clients answer. Room-owned fan-out tracks survive PeerConnection swaps
(reconnects). Packages:

| Package | Role |
|---|---|
| `media/sfu` | The SFU: per-track subscriptions, source injection, the **bidirectional port** (`AttachPort` — an external leg publishes into a room *and* hears it; one active publisher, no mixing) |
| `media/room` | Room/participant model + the extension seams (interfaces): `TrackSink` (tap audio → recording/STT), `ParticipantSource` (inject non-WebRTC Opus → IVR/phone/AI TTS), `Supervisor`, and **`Plug`/`Leg`** — THE PLUG, the channel-source contract (`interfaces.go`) |
| `media/signaling` | The `/rtc` WebSocket: auth, join, trickle-ICE relay, membership fan-out, IVR start, ring-to-agent, the plug wiring (`AttachPstn` in `pstn.go`), CDR hooks. Embeds the brain (`internal/brain`) |
| `media/ivrdrive` | Drives the unified IVR walker on the media plane (the one runtime driver) |
| `media/prompt` | Opus-in-Ogg prompt player (Injects prompts as a room participant) |
| `media/record` | Per-leg lossless capture → stereo Ogg merge (agent L / customer R) |

The brain itself lives in **`internal/brain`** (`Runner` = IVR session lifecycle,
`AgentRouter` = routing over the shared `internal/routing` engine, `RingTransport`
= the transport seam the plug supplies) — transport-neutral, called in-process by
the gateway. The `/rtc` wire contract is **frozen** (one JSON envelope, mirrored
in Go `rtc-protocol` + TS `rtc-protocol.ts`, guarded by `TestWireContractFrozen`).

## 5. Call sources (the pluggable adapters)

### 5a. Web — `/rtc` (the reference plane)
Browsers (visitor `voice.js`, agent `agent.js`) peer with the gateway over raw
WebRTC. No SIP, ever. The visitor presents a single-use gateway call token; the
agent presents an agent JWT. See the flow in §6a.

### 5b. Phone — the FreeSWITCH plug (seam ready; adapter is future work)
`room.Plug` + `room.Leg` (in `internal/media/room/interfaces.go`) is **THE
PLUG** — the channel-source contract: `OnInbound`/`Answer`/`Reject`/`Hangup`,
returning a `Leg` (Opus source in, room audio out via `Write`, DTMF channel,
`Done`). `signaling.AttachPstn(room.Plug, DIDResolver)` wires any plug into the
media plane; an inbound call becomes a room, resolves DID→tenant+flow, and runs
the **same** IVR/transfer/CDR as a web call.

The seam is **test-proven** today via `fakePstn` (`signaling_test.go`), but
**no FreeSWITCH adapter is wired yet** — the old ESL inbound path
(`cmd/orchestrator`) was deleted in the convergence migration, and the clean FS
plug package is future work (gated on provisioning a real SIP trunk). Outbound
PSTN still goes through FreeSWITCH today, originated by `cmd/api` over ESL.
**Swapping/adding a channel** = implementing `room.Plug` in a sibling package;
nothing in `internal/media` changes.

### 5c. WhatsApp (future)
Another `ParticipantSource`/adapter feeding the WhatsApp Business Calling media
into a room. No call-logic changes — that's the whole point of the seam.

## 6. End-to-end call flows

### 6a. Web visitor "call us"
```
click → [mic grabbed in gesture] → your backend mints token (API key)
  → POST /v1/guest-credentials → {gateway_url, flow_id(pinned), token(single-use), ice}
  → browser opens WSS /rtc?token= , sends join
  → gateway validates token (aud tring.gateway), burns jti (Redis SetNX, fail-closed)
  → room (namespaced by tenant) + SFU peer ; gateway OFFERS, browser answers
  → flow-pinned ⇒ IVR runs: greeting prompt injected as a participant, DTMF drives menus
  → CDR opened (callcdr.Started) ; recording sink attached if tenant records
  → transfer node rings online agents (Redis presence) → agent joins same room
  → CDR.Answered ; both legs bridged ; talking
  → hangup → CDR settled + recording finalized (stereo Ogg, optional S3)
```

**The same flow in plain language** (what happens from clicking the button). Note
a pure web call never touches FreeSWITCH — that's only for real phone legs (6b).
Mental model: a call is a **room of participants** (visitor, IVR robot, agent),
and the gateway's **SFU** is the switchboard everyone connects to once; it copies
each participant's audio to the others.

1. **Click.** The widget asks the browser for the microphone.
2. **Get a one-time ticket.** It fetches a short-lived, **single-use** call token
   from the CRM backend (`POST /v1/guest-credentials`). The token secretly pins
   *which IVR flow* runs, so the browser can't tamper with routing.
3. **One connection up.** The browser opens a single WebSocket to the gateway
   (`/rtc`) and sends `join` with the ticket.
4. **Gateway checks the ticket + opens a room.** It validates the token and burns
   its id so it can't be reused (if that check can't run, the call is refused —
   fail-closed). Then it creates a fresh room and adds the visitor as participant #1.
5. **Audio path is built (WebRTC handshake).** The **gateway offers, the browser
   answers** (the reverse of the usual assumption). Both trade ICE candidates
   (via STUN/TURN to get through firewalls/NAT) and do a DTLS-SRTP encryption
   handshake. The visitor's encrypted audio is now flowing into the SFU.
6. **IVR greets (if the flow has one).** The IVR joins the room as a participant
   and plays the greeting/menu — the visitor hears it via the SFU. Key presses go
   up as `dtmf` messages and drive the flow graph.
7. **Flow decides to reach an agent.** A `transfer` node picks who to ring: one
   agent, a team, or every online agent (ring-all). Or it dials a phone
   `number` over the outbound trunk (`ivrdial.go`) — often a `{{var}}` an
   earlier `http_callback` returned; the answered callee joins the room as a
   port in place of an agent, and busy/no answer takes `no_answer`.
   **Masking:** flows start with `{{caller}}` and `{{did}}` set, so a callback
   can pick a mask; `caller_id` on a number transfer (and on `POST /v1/calls`)
   presents one of the tenant's own numbers, and `label` replaces the caller's
   number on the agent's ring.
8. **The ring.** Each online agent already holds a quiet **presence socket**
   (registered at login). To ring them the gateway just **pushes an `incoming`
   message** down those sockets and their widget rings — no phone network, just a
   message on an open connection. Rings up to 30s, else `no_answer`.
9. **An agent accepts.** Their browser grabs its mic and opens its own `join` into
   the **same room** (same handshake as step 5). First to answer wins; the rest
   stop ringing.
10. **Connected.** The SFU now forwards visitor↔agent audio both ways. The CDR
    flips `ringing → answered`; if recording is on, each side is captured
    separately and merged to one stereo file.
11. **Hang up.** Either side sends `bye`; the room closes, the CDR is settled, the
    recording is finalized.

A brief socket blip won't drop the call: while the page stays open, a ~30s grace
window + reattach token (`media_grace_seconds`) lets the browser rejoin the same
room. A full page reload or a gateway restart *does* end it — live media can't be
relocated (see [State & high availability](/tring/architecture/state-and-ha/)).

### 6b. Inbound phone (target design — FS plug pending)
This is the shape the `room.Plug` seam is built for, once an FS adapter is wired
(no SIP trunk yet, so it isn't live):
```
telco → FreeSWITCH (terminate trunk, G.711↔Opus) → dialplan → FS plug adapter
  → answers Opus, exposes the call as a room.Leg → AttachPstn wires it to a room
  → resolve DID → tenant + flow (numbers.flow_id)
  → SAME IVR, SAME transfer-to-web-agent, SAME CDR + recording as 6a
```

The convergence in one line: **6b reuses 6a's room + IVR + transfer + CDR**; only
the entry adapter differs — and that adapter is the only FreeSWITCH-specific code.

## 7. IVR flows

Operators build flows in the console as a node graph (`vue-flow`), stored
per-tenant in `flows.definition` (JSONB: `{nodes, edges, entry_node}`).
`numbers.flow_id` attaches a flow to a DID. Node types: `start`, `greeting`,
`menu`, `gather`, `http_callback`, `transfer`, `goto_flow`, `timing`, `hangup`.

One **unified walker** (`internal/ivr.Walk`) has two consumers:
- **`media/ivrdrive`** — the one runtime driver; runs the flow on the pion plane
  (web today, any future plug — phone/WhatsApp — for free).
- **`brain/simulator.go`** — a deterministic, wall-clock-free walker that
  powers the console's Test panel (pure, fast tests). Interactive stepping pauses
  at `menu`/`gather` and resumes on a PATCHed digit. (The simulator lives in
  `internal/brain` — the standalone `internal/orchestrator` package was folded in
  and the ESL executor deleted.)

When adding a node type, update the walker, both consumers **and** the matching
Vue node component, or the editor and runtime diverge.

## 8. CDR & recording (`internal/callcdr`)

One CDR + recording path for **every** media-plane call (web + phone), reusing
the same `calls` table, spool dir, and S3 scheme as the FS path:

- **Started** → `calls` row (`ringing`), recording sink attached if
  `tenants.record_calls`.
- **Answered** → `answered_at` when an agent joins (agent leg also captured).
- **Journey** → the IVR walk stamped on `calls.ivr_path`.
- **Ended** → settle (status/duration/cause) + finalize recording.

Recording is **always stereo** (agent L / customer R; shrink via sample rate /
Opus, never mono). `recording_url` is published **only after the file lands**, so
a no-media call never shows a dead play button. S3 upload (date-wise key) +
`recording_s3_key` when `TRING_S3_BUCKET` is set; otherwise local spool. Served by
`GET /v1/calls/{id}/recording` (streams from S3 when keyed, else local).

> Both the gateway (uploader, deletes local after upload) and the api (serves
> recordings) need the S3 config — `docker-compose.yml` shares `.env` across both.

## 9. Auth — five distinct credentials (do not conflate)

| # | Credential | Holder | Scope |
|---|---|---|---|
| 1 | **Tenant API key** `tk_live_…` | integrator backend | full tenant; never in a browser |
| 2 | **Agent JWT** (aud `tring.sdk`, 5 min) | agent browser | `/rtc` agent WS + `/v1/sdk/*` |
| 3 | **Gateway call token** (aud `tring.gateway`, ~2 min, **single-use**) | visitor browser | one `/rtc` visitor call; flow pinned server-side; jti burned via Redis `SetNX` (fail-closed) |
| 4 | **Console JWT** (aud `tring.console`, 12 h) | operators | admin UI `/v1/…` |
| 5 | **FS Basic Auth** | FreeSWITCH | `/fs/*` mod_xml_curl callbacks |

The only token mint is server-side: `POST /v1/guest-credentials` (tenant API
key). There is no public/anonymous mint — `gateway_require_auth` defaults true.

## 10. Data model (`internal/migrate/migrations/*.sql`)

Source of truth = embedded migrations, applied on `cmd/api` startup. Every
non-`tenants` row carries `tenant_id` and queries MUST filter by it (RLS is a
later hardening). Tables:

`tenants` · `api_keys` · `agents` · `teams` · `team_agents` · `numbers`
(`type` ∈ inbound/outbound/masking; `flow_id`, `provider`, `priority`, `trunk_id`)
· `trunks` · `flows` (JSONB graph) · `calls` (CDR; `leg_id` = opaque leg id,
`source` ∈ web/phone/whatsapp, stereo `recording_url`/`recording_s3_key`,
`ivr_path`) · `masking_sessions` ·
`guest_sessions` · `webhooks` · `webhook_deliveries` · `users` (console operators)
· `audio_assets` (tenant sound library).

All runtime SQL lives in the root `queries.sql` catalog (listmonk-style), loaded
into a typed `Queries` struct (`internal/queries`) — no inline SQL in handlers.

## 11. Configuration (`internal/config`, koanf)

`defaults` map overlaid with `TRING_*` env vars; flat lowercase keys
(`TRING_ESL_ADDR` → `esl_addr`). Precedence flag > env > default. Add a setting in
**one place** (the `defaults` map) and read via `k.String("…")` — never
`os.Getenv` in new code. FreeSWITCH consumes its own `TRING_*` vars (e.g.
`TRING_PUBLIC_IP`, trunk creds) via `vars.xml`, not koanf. `audio_dir` and
`recordings_dir` default to `/var/lib/tring/{sounds,recordings}` — the gateway
writes/reads them and the API serves recordings; they are no longer under
`/usr/share/freeswitch` (FS doesn't touch them).

## 12. Deployment topology — public vs private

```
        PUBLIC (internet)                    │   PRIVATE (VPC / subnet)
 browser ─WebRTC media (UDP) ─┐              │
   visitor/agent ─HTTPS/WSS──▶│  GATEWAY     │                   ┌────────────┐
                              │  (web plug + │     ESL (outbound)│ FreeSWITCH │
 (symmetric NAT) ─▶ TURN ────▶│  brain)      │  ┌───────────────▶│ (PSTN edge)│
                              └──────────────┘  │                └────────────┘
 your backend ─API key──▶ API ◀── /fs ─────────┘
 operators ─VPN/SSO──▶ /admin            Postgres ──── API/gateway ──── Redis  (all PRIVATE)
```

| Component | Net | Ports |
|---|---|---|
| Gateway browser face | **public** | `8087`→TLS `443` (WSS `/rtc`), UDP media `TRING_GATEWAY_UDP_MIN`–`MAX` (def 10000–10100) on `TRING_PUBLIC_IP` |
| TURN (coturn) | **public** | `3478` UDP/TCP, `5349` TLS, relay UDP range |
| SDK bundles `/sdk/*.js` | **public** (CDN/proxy) | — |
| FreeSWITCH | **private** | SIP `5060`, RTP, ESL `8021` (outbound originate from the API) |
| API | **private** (recommended) | `8085` (server→server mint; `/admin` over VPN) |
| Postgres / Redis | **private** | `5432`/`5434`, `6379` |

`/rtc` (WebSocket) is the only browser-facing signaling endpoint. The future FS
PSTN plug (§5b) will add a private gateway↔FS SIP/RTP leg; it isn't wired today.

The **API can stay fully private**: the browser only needs the SDK JS (CDN), a
token (minted by your backend over server→server), and the gateway (public).

### Networking gotchas (the time-sinks)
- **Two gateway IPs, don't conflate:** `TRING_PUBLIC_IP` is browser-facing (pion
  rewrites the host ICE candidate to it — public/elastic IP). `TRING_GATEWAY_ADVERT_IP`
  is FS-facing (the SIP/RTP bind — must be locally assigned; keep `127.0.0.1` on
  AWS, you can't bind an elastic IP). `TRING_GATEWAY_PUBLIC_URL` is the
  browser-reachable signaling URL.
- **STUN is off and usually unneeded** — calls aren't P2P; the browser connects
  to the gateway (a public server), which learns the reflexive address itself
  (works through symmetric NAT). What matters: a correct `TRING_PUBLIC_IP` + open
  UDP range.
- **TURN is the real fallback** for UDP-blocked clients — run TURNS on 443. Use
  short-lived REST creds (shared `TRING_TURN_SECRET`), not static creds in a page.
- **Dev (one machine):** set `TRING_INTERNAL_IP=127.0.0.1` (+ matching FS/gateway
  loopback addrs) so a Wi-Fi change doesn't break every call.

### Capacity (rule of thumb)
~150–300 concurrent web calls on a 4-core/8GB box with Opus passthrough (CPU-bound
on the gateway's SRTP relay). Transcoding (recording) or co-located TURN cuts that.
Scale by adding **gateway** instances (horizontal); FS scales vertically.

> Call/media state is **in-process and not migratable** — scaling and restart
> behaviour follow from that. The state classification, cell-based scaling model,
> graceful-drain and orphan-reconciliation posture are in
> [State & high availability](/tring/architecture/state-and-ha/). Read it before adding a second cell or
> assuming calls survive a restart.

## 13. Testing

- `go test ./...` — pure unit suites (masking, dial, simulator, sfu, signaling)
  run with no Postgres.
- **DB-gated tests** skip unless `TRING_TEST_DSN` points at a DB whose name
  contains `test` (a destructive-truncate guard). Use a migrated `tring_test` on
  `:5434`. DB-gated packages share one DB + tenant — run them with `go test -p 1`
  to avoid a `tenants_sip_domain_key` collision.
- **Plug seam** is covered by `fakePstn` in `media/signaling/signaling_test.go`
  (inbound → plug → room → IVR), proving the channel-source contract end-to-end
  without FreeSWITCH. The live FS-trunk smoke test returns once an FS plug
  adapter is wired (§5b).
- `sim/` — a SIPp UAS impersonating a carrier SBC for the **outbound** trunk path.

## 14. Roadmap (what's next)

1. **FreeSWITCH PSTN plug** (§5b) — implement `room.Plug` as a clean FS adapter
   package so inbound trunk calls join the media plane (seam + `fakePstn` test
   already in place; gated on a real SIP trunk).
2. **Outbound origination on the plug** — click-to-call out the trunk via the
   media plane (the contract deliberately omits `Originate` so far), so the API's
   ESL originate path can also move behind the plug.
3. **WhatsApp adapter** (§5c) — needs Meta Business Calling API access.
4. **AI voice agent** — STT sink + TTS source on the plane that exists today.

## 15. Code map (where things live)

| Question | File |
|---|---|
| Browser call (visitor/agent) | `internal/media/signaling/` (`signaling.go`, `ivr.go`, `pstn.go`) + `media/sfu/` |
| The brain (IVR + routing, in-process) | `internal/brain/` (`ivr.go` `Runner`, `route.go` `AgentRouter`) |
| Plug seam (THE PLUG) | `internal/media/room/interfaces.go` (`room.Plug`/`Leg`); wiring `media/signaling/pstn.go` (`AttachPstn`); FS adapter = future (§5b) |
| CDR + recording (web; phone when plug lands) | `internal/callcdr/` + `media/record` |
| IVR walker (shared) | `internal/ivr/` ; consumers: `media/ivrdrive` (runtime), `brain/simulator.go` (Test panel) |
| Agent bridge dial-string | `internal/agentleg/` |
| Masking pool | `internal/masking/pool.go` |
| Trunk providers | `internal/provider/` |
| DB schema | `internal/migrate/migrations/*.sql` |
| SQL catalog | `queries.sql` + `internal/queries` |
| Config | `internal/config` (koanf) |
| Admin UI | `web/` (Vue 3), embedded via `internal/webui` |
| Browser SDK | `sdk/js/` → served at `/sdk/*` |
| FS dialplan / plug | `freeswitch/conf/dialplan/default.xml`, `sip_profiles/external/tring-gw.xml` |

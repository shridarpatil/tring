# Tring — Technical Specification

> Status: living document. Describes the system as built. Where a component
> is a scaffold or planned, it says so explicitly.

## 1. What Tring is

Tring is an **open-source, API-first voice CPaaS** — think "self-hostable Twilio Voice, India-primary." An application developer integrates voice by dropping a `<script>` tag (and optionally a button) into their web app; their agents and/or website visitors can then place and receive real phone calls and WebRTC calls. Tring orchestrates everything via **FreeSWITCH** under the hood, but the integrator never sees FreeSWITCH or SIP.

### Locked decisions
- **License:** Apache 2.0.
- **Tenancy:** multi-tenant from day one (per-tenant SIP domains, dynamic directory via `mod_xml_curl`).
- **Delivery:** hosted SaaS **and** self-host bundle — both from the same binaries. Self-host is a first-class target: no feature should assume the SaaS environment.
- **Geography:** India-primary (Exotel/Airtel/Jio/Tata trunks, DLT compliance on roadmap); international via a provider abstraction.

### Goals
- Drop-in browser voice (agent dialer + visitor "call us" widget) with zero telephony knowledge required of the integrator.
- WebRTC ↔ PSTN bridging, IVR flows, call recording (roadmap), CDRs, webhooks.
- Multi-tenant isolation with per-tenant config managed from an admin console.
- Runnable as a single `docker compose up` for self-hosters.

### Non-goals (currently)
- Video calling.
- SMS/messaging (would be a separate plane — see §13).
- A native mobile SDK (the JS SDK is browser-only; native is a roadmap item — see §11).

---

## 2. High-level architecture

Three Go binaries, one Postgres, one Redis, one **private** FreeSWITCH (ADR-001):

```
   Browser (raw WebRTC)                ┌──────────────────────────────────────────┐
   ───────────────────────────────►    │                 cmd/gateway                │  PUBLIC edge
   HTTPS /webrtc/offer (visitor)        │  pion WebRTC ⇄ browser (DTLS-SRTP/Opus)    │  (the only call-path
   WSS  /agent/ws    (agent)            │  sipgo B2BUA ⇄ FreeSWITCH (plain RTP+SIP)  │   thing on the internet)
        │  ICE relay                    └───────────────┬────────────────────────────┘
        ▼                                               │ SIP :5090 + RTP        ▲ presence (Redis)
   ┌────────┐                                           ▼                        │
   │  TURN  │                                  ┌──────────────┐  ESL    ┌──────────────────┐
   └────────┘                                  │  FreeSWITCH  │◄───────►│ cmd/orchestrator │  PRIVATE
                                               │  (PRIVATE)   │  events │  :8084 masking   │
   your backend ─server→server─► ┌─────────┐   │  SIP/RTP/IVR │  :8086  │  :8086 IVR exec  │
   (mints tokens, API key)        │ cmd/api │   └──────┬───────┘  socket └──────────────────┘
   operators ──────────────────► │ REST /v1│          │ trunks
                                  │ /fs /sdk│       PSTN (Exotel/Airtel/Jio/Tata)
                                  │ /admin  │
                                  └────┬────┘
                            Postgres ──┘── Redis (flow cache + agent presence)
```

### The three planes
1. **`cmd/api`** — the *public* HTTP surface (no media). Tenant/agent CRUD, JWT + gateway-call-token + API-key minting, REST call endpoints, webhook registration, the embedded admin console, and the embedded browser widgets. **Stateless, horizontally scalable.** Never talks to FreeSWITCH call control directly — it tells the orchestrator what to do (and uses fresh ESL connections only for fire-and-forget `originate`). Can run fully private (see §6).
2. **`cmd/orchestrator`** — the *FreeSWITCH-facing* control service. Holds an inbound ESL connection (events), runs outbound-ESL listeners for dialplan socket hand-offs (`:8084` masking, `:8086` IVR), manages in-process call sessions, writes CDRs on hangup. **State-aware, scaled vertically per FreeSWITCH instance.**
3. **`cmd/gateway`** — the *browser-facing* WebRTC media gateway (ADR-001). Terminates the browser leg (pion: DTLS-SRTP/ICE/Opus) and bridges it into the **private** FreeSWITCH as a SIP B2BUA (sipgo). Browsers never speak SIP and FreeSWITCH is never internet-exposed. **State-aware (per-call bridges), scaled horizontally per FreeSWITCH instance.** See §3.

Each plane scales differently — that's why they're separate processes.

### Ports (defaults)
| Service | Port | Purpose |
|---|---|---|
| API | 8085 | REST, console, `/sdk/*`, `/fs/*` |
| Gateway | 8087 | browser signaling: `/webrtc/offer` (HTTP), `/agent/ws` (WebSocket) |
| Gateway | 10000–10100/udp | WebRTC media (browser ⇄ gateway) — widen for scale |
| Gateway | 5090 | SIP UAC/UAS toward the private FreeSWITCH (`tring-gw` peer) |
| Orchestrator | 8084 | masking outbound-ESL socket |
| Orchestrator | 8086 | IVR executor outbound-ESL socket |
| FreeSWITCH | 5060 | SIP (internal profile) — PRIVATE |
| FreeSWITCH | 5080 | SIP (external profile / trunks) — PRIVATE |
| FreeSWITCH | 8021 | ESL (control) — PRIVATE |
| Postgres | 5434 (dev) | database |
| Redis | 6379 | flow cache + agent presence |
| TURN (coturn) | 3478 / 5349 | NAT relay (prod) |

> FreeSWITCH's old SIP-over-WebSocket bindings (`5066`/`7443`) are no longer used — the gateway replaced the browser SIP path in the M5 cutover.

---

## 3. WebRTC / media architecture (the media gateway)

Since the **M5 cutover (ADR-001)**, browsers do **not** speak SIP and never touch
FreeSWITCH. They peer with `cmd/gateway`, a **B2BUA** that terminates the browser
WebRTC leg and bridges it into a **private** FreeSWITCH over plain RTP + SIP.

```
 Browser ──DTLS-SRTP / Opus (UDP)──►  GATEWAY  ──plain RTP──►  FreeSWITCH (private)
         ──HTTP/WS signaling──────►  (pion)   ──SIP (sipgo)─►  IVR / trunk / bridge
```

Two browser-facing roles, both on `:8087`:
- **Visitor** (`POST /webrtc/offer`, HTTP): the browser POSTs an SDP offer; the
  gateway dials FreeSWITCH (`gwflow` extension, flow pinned via the
  `X-Tring-Flow-Id` SIP header) and returns the SDP answer.
- **Agent** (`/agent/ws`, WebSocket): the agent holds one authenticated socket
  for presence + incoming-call signaling. FreeSWITCH delivers an agent leg to the
  gateway as a SIP endpoint (`sofia/gateway/tring-gw/<sip>`; gateway is the UAS),
  which rings the browser over the WS; the browser offers WebRTC and the gateway
  answers + bridges.

**Media relay** (`internal/gateway/gateway.go::relay`): the gateway terminates the
browser's DTLS-SRTP, strips WebRTC-only RTP header extensions FreeSWITCH didn't
negotiate, and **re-marks the payload type per codec** (browser Opus PT → FS's
Opus PT; telephone-event → FS's DTMF PT) so Opus passes through with **no
transcoding** and RFC 4733 DTMF survives. The FreeSWITCH leg is plain RTP on the
private network (no DTLS). Two goroutines per call (browser→FS, FS→browser).

**Codecs:** Opus end-to-end (browser ⇄ gateway ⇄ FS), so FS bridges Opus↔Opus as
pass-through; transcoding to G.711 only happens on the PSTN trunk leg.

**ICE / TURN:** assembled server-side (`buildICEServers`) from `TRING_STUN_URL` /
`TRING_TURN_*` and handed to the browser (and used by the gateway's own
candidates). The gateway advertises `TRING_PUBLIC_IP` as its host candidate.
**TURN is required for symmetric-NAT / CGNAT / cellular clients** (prod
prerequisite; prefer short-lived TURN REST credentials).

**Lifecycle / teardown:** the bridge tears down on (a) the agent's WS
`hangup`/`reject`, (b) FreeSWITCH ending the leg (a SIP **BYE** to the gateway's
client/server dialog — both `OnBye`-routed and watched via the dialog's
`StateRead()`), or (c) the browser PeerConnection failing/closing. Teardown is
idempotent (`sync.Once`): closes the pc + RTP socket and BYEs the FS leg.

### 3.1 End-to-end calling stack (libraries)

| Layer | Library / tech | Where |
|---|---|---|
| Browser media | native `RTCPeerConnection` + `getUserMedia` (no framework; **sip.js removed**) | `sdk/js/src/{gateway,agent-gateway}.ts` |
| Browser signaling | native `fetch` (visitor offer) + native `WebSocket` (agent) | same |
| Gateway WebRTC | **pion/webrtc v4** (`v4.2.9`) — DTLS-SRTP, ICE, MediaEngine, `TrackLocalStaticRTP` | `internal/gateway/gateway.go` |
| └ pion media deps | pion `rtp`, `sdp/v3`, `srtp/v3`, `dtls/v3`, `ice/v4` (transitive) | — |
| Gateway ⇄ FreeSWITCH SIP | **emiago/sipgo** (`v1.4.0`) — UAC dial + UAS receive (DialogClient/ServerCache) | `internal/gateway/{fsleg,fsuas}.go` |
| Agent presence/signaling WS (server) | **gobwas/ws** (`v1.3.2`) | `internal/gateway/agent.go` |
| FreeSWITCH control (events + originate) | **percipia/eslgo** (`v1.5.0`) — ESL | `internal/orchestrator`, `internal/freeswitch` |
| Call tokens / agent JWT | **golang-jwt/jwt v5** (`v5.3.1`) HS256 | `internal/api/jwt.go`, `internal/gateway/auth.go` |
| HTTP routing (api) | **go-chi/chi v5** (`v5.2.5`) | `internal/api` |
| Postgres | **jackc/pgx v5** (`v5.9.2`) + golang-migrate (`v4.19.1`) | `internal/db`, `internal/migrate` |
| Redis (flow cache + presence) | **redis/go-redis v9** (`v9.20.0`) | `internal/cache`, `internal/presence` |
| Config | **knadh/koanf v2** (`v2.3.5`) | `internal/config` |
| Console UI | Vue 3 + Vue Router + Tailwind + Vite + **vue-flow** (IVR editor) | `web/` |
| SDK build | TypeScript + **esbuild** | `sdk/js` |
| TURN (prod) | **coturn** (recommended) | external |

### 3.2 FreeSWITCH

FreeSWITCH is the media/SIP switch — **private** (no public IP). Dev image:
`safarov/freeswitch`. Relevant pieces:

- **mod_sofia** — SIP stack. The *internal* profile (`:5060`) hosts agents + the
  gateway's auth user; the *external* profile (`:5080`) hosts trunk gateways and
  the **`tring-gw`** peer (`sip_profiles/external/tring-gw.xml`) that FreeSWITCH
  routes agent legs to (proxy = `$${tring_gateway_sip}`).
- **mod_xml_curl** — dynamic directory + dialplan: FreeSWITCH `POST`s
  `/fs/directory` to the API on every SIP auth (no static user files). See §5.
- **mod_event_socket (ESL)** — `:8021`; the orchestrator's control channel and
  the API's originate path (via eslgo).
- **Dialplan** (`dialplan/default.xml`): `tring_gateway_flow` (`gwflow`) sets
  `tring_flow_id`/`tring_client_ref` from the gateway's SIP headers then hands the
  call to the IVR executor socket (`:8086`); `tring_ivr` (`^\d+$`) does the same
  for DIDs; trunk/masking extensions for PSTN.
- **Codecs:** `global_codec_prefs=OPUS,…`; Opus pass-through with the gateway,
  G.711 transcode on trunks.
- **RTP:** plain RTP on the private network to the gateway; DTLS-SRTP terminates
  at the gateway, not FreeSWITCH.

---

## 4. Data model (Postgres)

Source of truth: `internal/migrate/migrations/*.sql` (applied by `golang-migrate` on `cmd/api` startup; tracked in `schema_migrations`).

| Table | Purpose |
|---|---|
| `tenants` | The unit of isolation. `sip_domain`, `outbound_strategy`, `failover_causes`, `public_key` (widget), `support_flow_id`, `allowed_origins`, `guest_require_backend_mint`. |
| `users` | Console operators (email + bcrypt). |
| `api_keys` | Tenant API keys (`tk_live_…`): multiple named keys, SHA-256 hash, prefix, `last_used_at`, soft `revoked_at`. |
| `agents` | SIP identities owned by a tenant (`sip_username`, `sip_password`). |
| `numbers` | DIDs: `type ∈ {inbound, outbound, masking}`, `provider`, `priority`, `active`, `flow_id`, `trunk_id`. |
| `trunks` | UI-managed SIP trunks (provider, register vs IP-auth, proxy, active). |
| `flows` | IVR flow definitions (JSONB graph: nodes/edges/entry_node), `name`, `is_active`. |
| `calls` | CDRs: `direction`, `status`, `from/to_number`, `client_ref`, `agent_id`, `fs_uuid`, `fs_job_uuid`, `ivr_path`, timing, `hangup_cause`, `recording_url`. |
| `webhooks` / `webhook_deliveries` | Subscriptions + per-delivery audit. |
| `masking_sessions` | Durable mirror of the in-memory masking pool (recovery store). |

> `guest_sessions` was **dropped** (migration 0008): visitor web calls are
> stateless now — a short-lived gateway call token replaces the ephemeral SIP
> identity (see §8). Agent presence lives in Redis, not the DB.

**Tenancy enforcement is application-level** today (every non-`tenants` query filters `tenant_id`); PostgreSQL RLS is a Phase 2 hardening.

---

## 5. Multi-tenancy & the dynamic directory

There are no static SIP user files. FreeSWITCH calls **`POST /fs/directory`** (`mod_xml_curl`) on every SIP auth; `internal/api/handlers_fs.go`:
1. Resolves the tenant by `sip_domain`.
2. Resolves the agent by `(tenant_id, sip_username)`.
3. Returns FreeSWITCH XML with the SIP password and channel variables (`tenant_id`, `agent_id`, `user_context`).

This serves agents (legacy SIP transport) and the **gateway's auth user** (the directory user the gateway digest-authenticates as for the visitor leg). The old `guest_*` directory branch was removed with `guest_sessions` — visitor calls no longer register SIP. A domain-level `dial-string` param is included so `originate user/<id>@<domain>` can construct a dial URI.

---

## 6. Authentication surfaces

Five distinct schemes — do not conflate:

| Scheme | Who | Gates |
|---|---|---|
| **Tenant API key** (`tk_live_…`, SHA-256 in `api_keys`) | Integrator backend (server-to-server) | `/v1/*` tenant routes (`AuthRequired`), incl. token/guest minting |
| **Console JWT** (HS256, 12h, aud `tring.console`) | Operators, via `POST /v1/console/login` (email+bcrypt) | `/v1/*` tenant routes (`AuthRequired`) |
| **Agent JWT** (HS256, 5-min, aud `tring.sdk`) | The agent's browser, minted by `GET /v1/agents/{id}/token` | the gateway WebSocket `/agent/ws` + `/v1/sdk/*`, scoped to the subject agent |
| **Gateway call token** (HS256, ~2-min, aud `tring.gateway`) | A visitor's browser, minted by the guest endpoints | the gateway `/webrtc/offer`; carries the pinned `flow_id` + (trusted mint) `client_ref` |
| **FS Basic Auth** (`fs:secret`) | FreeSWITCH → API | `/fs/*` callbacks (private trust boundary) |

The browser never holds tenant-wide credentials. The agent JWT and gateway call token are minted by the integrator's backend (or Tring's public guest mint). **The API can run fully private**: the browser needs only the SDK bundle (CDN/proxy), a token (from the integrator backend over the private network), and the gateway — so nothing forces the API onto the internet (see `docs/EDGE_DEPLOYMENT.md`).

---

## 7. Outbound call path (agent → customer)

`POST /v1/sdk/calls` (agent JWT) → `internal/freeswitch.Client.Originate` → fresh ESL connection → `bgapi originate <vars><A-leg> <provider B-leg>`:

1. **A-leg** depends on `agent_transport` (`internal/api/handlers_calls.go::agentALeg`):
   - **`gateway`** (default since the cutover): `[sip_h_X-Tring-Tenant=<t>]sofia/gateway/tring-gw/<sip>` — FreeSWITCH sends the agent leg to the media gateway, which rings the agent's browser over its WebSocket.
   - **`sip`** (legacy): `user/<sip>@<domain>` — FreeSWITCH rings a SIP-registered contact (the SDK no longer supports this).
2. **B-leg** = the trunk, chosen by `internal/provider` + `internal/api/trunks.go::selectTrunks` per the tenant's `outbound_strategy` (`failover` | `round_robin` | `manual` | `load_based`*). Caller-ID stamped per-leg.
3. A `calls` row is written `ringing` with **two** FS identifiers: `fs_uuid` (pre-allocated channel) and `fs_job_uuid` (bgapi job). The orchestrator settles the row by `fs_uuid` on `CHANNEL_*` events, or by `fs_job_uuid` on `BACKGROUND_JOB` when the channel never gets created (e.g. agent not registered → `MANDATORY_IE_MISSING`).

`*load_based` is a reserved enum (returns 400 "not implemented").

---

## 8. Visitor web-calling path (visitor → IVR → agent)

The customer-facing counterpart to the agent dialer. A website visitor with **no account** calls into a tenant's IVR over WebRTC — straight to the gateway, no SIP.

```
Customer site
  ├─ ANONYMOUS:  data-gateway-url + data-flow-id (no token)
  └─ LOGGED-IN:  data-credentials-url → your backend → POST /v1/guest-credentials
                 (API key + verified client_ref) → { gateway_url, flow_id, token, ice }
  │
  └─ POST /webrtc/offer (SDP [+ Bearer call token]) ─► GATEWAY
        └─ validates token (flow_id, client_ref) ─► dials FS gwflow
             X-Tring-Flow-Id + X-Tring-Client-Ref ─► IVR executor :8086
                └─ resolveFlowByID(tring_flow_id) → walks the flow graph
                     └─ Transfer node → ring online agents → agent answers
```

Key properties:
- **Flow-pinned server-side:** the flow id rides in the signed gateway call
  token (trusted mint) or the page config (anonymous) — the visitor never
  chooses a destination, and can never reach a trunk (the `gwflow` dialplan
  only sockets to the IVR).
- **Trusted `client_ref`:** the API-key mint (`POST /v1/guest-credentials`,
  `internal/api/handlers_guest.go`) embeds a server-verified `client_ref` in the
  token; the gateway propagates it (`X-Tring-Client-Ref`) and the executor
  records it on the CDR. The browser can never set it. The public mint refuses
  `client_ref`. See §10.
- **"To" on the CDR:** a web call has no dialed number, so the executor records
  the **flow name** (not the internal `gwflow` token) as `to_number`.
- **Agent answering:** the IVR `Transfer` node rings only agents **currently
  connected to the gateway** (presence-filtered; a specific agent via
  `config.agent_sip`, or all online agents simultaneously). The agent widget
  shows an **incoming-call screen (Accept/Decline)** (`autoAnswer:false`); the
  agent's own *outbound* leg auto-answers.

---

## 9. IVR flow engine

Operators build flows in the console as a **node graph** (vue-flow), stored per-tenant in `flows.definition` (JSONB v2: `{nodes, edges, entry_node}`; each node has `id`, `type`, `label`, `position`, `config`).

Two consumers of the same graph must stay behaviorally aligned:
1. **Executor** (`internal/orchestrator/ivr_executor.go`) — walks the graph on a *real* call, emitting ESL commands (playback, `play_and_get_digits`, bridge). Runs as an outbound-ESL listener on `:8086`.
2. **Simulator** (`internal/orchestrator/simulator.go`) — a deterministic, wall-clock-free walker that traces what *would* happen, powering the console's Test panel (incl. interactive step-through that pauses at Menu/Gather for live digit input).

**Node maturity:**

| Node | Executor status |
|---|---|
| `start`, `greeting`, `menu`, `hangup` | ✅ implemented |
| `transfer` | ✅ rings agents (specific or all) |
| `gather`, `http_callback`, `timing`, `goto_flow` | ⚠️ scaffold stubs (log "not yet implemented", fall through) |

Adding a node type means updating **executor + simulator + the Vue node component** together.

---

## 10. Security model (visitor calling)

The public mint (`/v1/sdk/guest-credentials`) and the gateway are defended in layers:

| Control | Stops |
|---|---|
| **Origin allowlist** (`tenants.allowed_origins`, fail-closed) | Embedding a stolen public key on another site (Origin is unspoofable in a browser) |
| **Per-IP rate limit** (10/min, `internal/api/ratelimit.go`) | Scripted mint floods from one host |
| **Flow-pinned token (no `client_ref` from a browser)** | Choosing another flow; spoofing a caller identity (only the API-key mint sets `client_ref`) |
| **`gwflow` dialplan only sockets to the IVR** | **Toll fraud** — a web call can never reach a trunk bridge |
| **Short-lived gateway call token (~2-min TTL)** | Token replay / arbitrary dialing |
| **`gateway_require_auth`** (optional) | Anonymous visitors entirely (require a minted token on `/webrtc/offer`) |

Net: a stolen key is blocked in a browser; even a scripted spoof can only make **rate-limited, money-safe nuisance calls into the tenant's own IVR**. Operators manage the allowlist + support flow + `guest_require_backend_mint` from **Settings → Guest web calling** (`GET/PATCH /v1/guest-config`).

**Production prerequisites** (not enforced by code): TLS-fronting the gateway signaling (HTTPS + WSS on 443), a TURN server (prefer short-lived TURN REST credentials), and bot mitigation in front of the mint. See `docs/EDGE_DEPLOYMENT.md`.

---

## 11. Browser SDK & widgets

`sdk/js/` (TypeScript, esbuild). **No runtime dependencies** — sip.js was removed in the cutover; the SDK uses native `RTCPeerConnection` + `WebSocket`, so the bundles dropped from ~500 KB to ~22 KB. **The API serves the bundles at `/sdk/*`** (embedded via `internal/widget`, `//go:embed`) so self-hosters need no CDN.

| Bundle | Served | For | Transport |
|---|---|---|---|
| `voice.js` | `/sdk/voice.js` | website visitor → IVR | `GatewayAdapter` — raw WebRTC, POST `/webrtc/offer` (`data-gateway-url`+`data-flow-id`, or `data-credentials-url`) |
| `agent.js` | `/sdk/agent.js` | CRM agent dialer + incoming | `AgentGateway` — agent JWT over `/agent/ws` + WebRTC (`data-token`/`data-token-url` + `data-gateway-url`) |
| `tring.js` | `/sdk/tring.js` | bare SDK (`window.Tring`) | — |
| `tring.esm.js` | npm | bundler ESM | — |

Source: `gateway.ts` (visitor adapter), `agent-gateway.ts` (agent WS+WebRTC), `tring.ts`/`guest.ts` (controllers). The embed snippet derives its origin from the page. **Native mobile** isn't supported by the JS SDK; the path there is a native client (`react-native-webrtc`/`flutter-webrtc` + CallKit/ConnectionService) reusing the **same gateway protocol** — see §3.

---

## 12. Trunk provider abstraction

Trunks are **UI-managed** (`/v1/trunks`), rendered to per-trunk gateway XML under `sip_profiles/external/dynamic/` and live-rescanned (zero-downtime). Indian providers (`exotel`, `airtel`, `jio`, `tata`) share `indianSIPTrunk` (`internal/provider/india.go`): same E.164 normalization (`internal/dial`, rejects non-Indian at the boundary), same gateway shape. Adding a provider is a small, contained change; switching a tenant's trunk is config-only.

Outbound combination per call is governed by `tenants.outbound_strategy` (failover / round_robin / manual / load_based), assembled in `originateForAgent` + `selectTrunks`.

---

## 13. Webhooks

`internal/webhook.Notifier` (used by both planes) emits `call.ringing` / `call.answered` / `call.ended`. Per-subscriber `webhook_deliveries` rows; goroutine-per-delivery with retry/backoff (6 attempts). **Stripe-style signing**: `Tring-Signature: t=<unix>,v1=<hmac_sha256(secret, "<t>.<body>")>` + `Tring-Event` + `Tring-Delivery`. Best-effort by design (never blocks the call path). Crash recovery for in-flight deliveries is Phase 2.

---

## 14. Deployment

- **Single `docker compose up -d`** brings up FreeSWITCH + Postgres + Redis + api + orchestrator + **gateway**. Images embed the console + widget bundles (Dockerfile multi-stage: Node builds Vue + SDK → Go embeds → distroless; `CMD=api|orchestrator|gateway`).
- **Config via env** (koanf, `TRING_*`): DSN, Redis, JWT secret, FS basic auth, bootstrap tenant/admin, STUN/TURN. Gateway-specific: `TRING_GATEWAY_PUBLIC_IP` (browser host candidate), `TRING_GATEWAY_ADVERT_IP` (private IP toward FS), `TRING_FS_SIP_ADDR`, `TRING_GATEWAY_PUBLIC_URL`, `TRING_GATEWAY_UDP_MIN/MAX`, `TRING_AGENT_TRANSPORT`.
- **Gateway runs `network_mode: host`** (like FreeSWITCH) for clean RTP/SIP media. FreeSWITCH binds `local_ip_v4`, so the gateway's `TRING_FS_SIP_ADDR`/`ADVERT_IP` must be the host LAN IP, not `127.0.0.1`; a host-IP change requires recreating `freeswitch` + `gateway`.
- **Topology** (what's public vs private) and the production hardening checklist live in **`docs/EDGE_DEPLOYMENT.md`**. In short: only the **gateway** (signaling + media) and **TURN** face the internet; FreeSWITCH, orchestrator, Postgres, Redis — and optionally the API — stay private.
- **Migrations** run on api startup (idempotent). **Bootstrap** seeds a tenant + admin user on an empty DB. FreeSWITCH config is bind-mounted `:ro` (the base image's entrypoint clobbers RW mounts).

---

## 15. Known limitations & roadmap

| Area | Current | Planned |
|---|---|---|
| Web calling | gateway (raw WebRTC), sip.js removed; agent + visitor both live | — (cutover complete) |
| Media validation | proven on a single host (dev) | multi-gateway load test; capacity numbers |
| Visitor hangup | agent + FS-side ends are instant; visitor tab-close waits on ICE failure | `sendBeacon` "bye" on `unload` |
| TURN | optional, static creds | short-lived TURN REST creds wired into the mint |
| IVR nodes | start/greeting/menu/transfer/hangup | gather, http_callback, timing, goto_flow |
| Tenancy | app-level filtering | PostgreSQL RLS |
| Masking pool | in-memory (lost on restart) | mirror to `masking_sessions` |
| Recording | `recording_url` column exists | capture wiring |
| Webhooks | in-process goroutines | durable worker (poll pending) |
| Mobile | browser SDK only | native client (CallKit/ConnectionService) over the gateway protocol |
| Messaging | none | separate SMS plane (not FreeSWITCH) |
| Call transfer | IVR transfer-to-agent | agent-initiated blind/attended transfer |

---

## 16. Code map (where to look)

| Question | File |
|---|---|
| WebRTC media gateway | `internal/gateway/` — `gateway.go` (relay/bridge), `server.go` (offer), `agent.go` (agent WS), `fsleg.go` (UAC dial), `fsuas.go` (UAS receive), `auth.go` (tokens) |
| Outbound call origination | `internal/freeswitch/esl.go`, `internal/api/handlers_calls.go` (`agentALeg`) |
| Inbound call → Go | `cmd/orchestrator/main.go`, `internal/orchestrator/ivr_executor.go` |
| Agent presence | `internal/presence/` (Redis), published by the gateway, read by api + orchestrator |
| Masking/DID pool | `internal/masking/pool.go` |
| Dynamic SIP directory | `internal/api/handlers_fs.go` |
| Visitor / guest mint | `internal/api/handlers_guest.go` (gateway call token), dialplan `tring_gateway_flow` (`gwflow`) |
| API keys | `internal/api/handlers_apikeys.go`, `keys.go`, `auth.go` |
| IVR executor / simulator | `internal/orchestrator/{ivr_executor,simulator,sim_session}.go` |
| Trunk selection | `internal/api/trunks.go`, `internal/provider/` |
| Browser SDK (gateway transport) | `sdk/js/src/{gateway,agent-gateway,tring,guest}.ts` |
| FreeSWITCH gateway peer | `freeswitch/conf/sip_profiles/external/tring-gw.xml`, `vars.xml` (`tring_gateway_sip`) |
| Admin console | `web/` (Vue 3), embedded via `internal/webui` |
| Widget serving | `internal/widget` (`/sdk/*`) |
| Schema | `internal/migrate/migrations/*.sql` |

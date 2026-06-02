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

Two Go binaries, one Postgres, one FreeSWITCH:

```
                         ┌─────────────────────────────────────────┐
   Browser (sip.js)      │                 cmd/api                  │
   ───────────────►  WSS │  REST /v1  ·  /fs (FS callbacks)         │
   widget / console      │  /admin (Vue console)  ·  /sdk/* widgets │
                         │  stateless · horizontally scalable       │
                         └───────────────┬───────────┬─────────────┘
                                         │           │
                                   Postgres      (tells orchestrator
                                         │         what to do; never
                         ┌───────────────┴──────┐   talks to FS directly)
                         │      cmd/orchestrator │
   FreeSWITCH  ◄──ESL──► │  inbound ESL (events) │
   (media + SIP-WS)      │  :8084 masking socket │
        │                │  :8086 IVR executor   │
        │                │  state-aware sessions │
   PSTN trunks           └───────────────────────┘
   WebRTC peers
```

### The two-plane split
1. **`cmd/api`** — the *public* HTTP/WSS surface. Tenant/agent CRUD, JWT minting, REST call endpoints, webhook registration, the embedded admin console, and the embedded browser widgets. **Stateless, horizontally scalable.** Never talks to FreeSWITCH's call control directly — it tells the orchestrator what to do (and uses fresh ESL connections only for fire-and-forget `originate`).
2. **`cmd/orchestrator`** — the *FreeSWITCH-facing* service. Holds an inbound ESL connection (events), runs outbound-ESL listeners for dialplan socket hand-offs (`:8084` masking, `:8086` IVR), manages in-process call sessions, writes CDRs on hangup. **State-aware, scaled vertically per FreeSWITCH instance.**

The split matters because the API plane scales differently from the call-orchestration plane.

### Ports (defaults)
| Service | Port | Purpose |
|---|---|---|
| API | 8085 | REST, console, `/sdk/*`, `/fs/*` |
| Orchestrator | 8084 | masking outbound-ESL socket |
| Orchestrator | 8086 | IVR executor outbound-ESL socket |
| FreeSWITCH | 5060 | SIP (internal profile) |
| FreeSWITCH | 5080 | SIP (external profile / trunks) |
| FreeSWITCH | 8021 | ESL (control) |
| FreeSWITCH | 5066 | SIP-over-WebSocket (ws) |
| FreeSWITCH | 7443 | SIP-over-WebSocket secure (wss) — WebRTC |
| Postgres | 5434 (dev) | database |

---

## 3. WebRTC / media architecture

A WebRTC call is **two channels**: *signaling* and *media*.

- **Signaling = SIP over WebSocket (RFC 7118).** The browser is "just another SIP endpoint." Library: **`sip.js`** (third-party, MIT; `sdk/js/package.json`), wrapped by our thin `SipAdapter` in `sdk/js/src/sip.ts`. It registers, sends INVITEs, and feeds SDP into the browser's WebRTC engine.
- **Media = native browser WebRTC.** Encrypted RTP (**SRTP**), keyed by **DTLS**, codec **Opus** (preferred — `vars.xml: global_codec_prefs=OPUS,G722,PCMU,PCMA`), with **ICE** (STUN/TURN) for NAT traversal. No media library — it's the browser's `RTCPeerConnection`/`getUserMedia`.
- **The other end = FreeSWITCH**, which terminates the WebRTC leg (ICE/DTLS/SRTP), runs the IVR, and bridges to other WebRTC legs or to PSTN trunks (transcoding Opus→G.711).

Why SIP-over-WS rather than custom signaling: FreeSWITCH is a SIP switch, so a browser leg and a phone leg are uniform — the same call control, IVR, trunking, transfer, and CDR logic applies to both, enabling seamless WebRTC↔PSTN bridging.

**ICE servers** are configured server-side (`TRING_STUN_URL`, `TRING_TURN_*`), assembled in `internal/api/server.go::buildICEServers`, and handed to the browser via the credentials endpoints (passed verbatim into `RTCPeerConnection`). STUN-only works on LANs/simple NAT; **TURN is required for customers on symmetric NAT / CGNAT / cellular** (production prerequisite).

---

## 4. Data model (Postgres)

Source of truth: `internal/migrate/migrations/*.sql` (applied by `golang-migrate` on `cmd/api` startup; tracked in `schema_migrations`).

| Table | Purpose |
|---|---|
| `tenants` | The unit of isolation. `sip_domain`, `outbound_strategy`, `failover_causes`, `public_key` (widget), `support_flow_id`, `allowed_origins`. |
| `users` | Console operators (email + bcrypt). |
| `agents` | SIP identities owned by a tenant (`sip_username`, `sip_password`). |
| `numbers` | DIDs: `type ∈ {inbound, outbound, masking}`, `provider`, `priority`, `active`, `flow_id`, `trunk_id`. |
| `trunks` | UI-managed SIP trunks (provider, register vs IP-auth, proxy, active). |
| `flows` | IVR flow definitions (JSONB graph: nodes/edges/entry_node), `is_active`. |
| `calls` | CDRs: `direction`, `status`, `from/to_number`, `agent_id`, `fs_uuid`, `fs_job_uuid`, timing, `hangup_cause`, `recording_url`. |
| `webhooks` / `webhook_deliveries` | Subscriptions + per-delivery audit. |
| `masking_sessions` | Durable mirror of the in-memory masking pool (recovery store). |
| `guest_sessions` | Ephemeral anonymous SIP identities for visitor calling (15-min TTL). |

**Tenancy enforcement is application-level** today (every non-`tenants` query filters `tenant_id`); PostgreSQL RLS is a Phase 2 hardening.

---

## 5. Multi-tenancy & the dynamic directory

There are no static SIP user files. FreeSWITCH calls **`POST /fs/directory`** (`mod_xml_curl`) on every SIP auth/registration; `internal/api/handlers_fs.go`:
1. Resolves the tenant by `sip_domain`.
2. Resolves the agent by `(tenant_id, sip_username)` — or a guest by `guest_*` username against `guest_sessions`.
3. Returns FreeSWITCH XML with the SIP password and channel variables (`tenant_id`, and for guests `user_class=guest` + `tring_flow_id`).

Adding an agent (or minting a guest session) makes them immediately registerable — no FreeSWITCH config edit or reload. A domain-level `dial-string` param is included so `originate user/<id>@<domain>` can construct a dial URI from the registered contact.

---

## 6. Authentication surfaces

Four distinct schemes — do not conflate:

| Scheme | Who | Gates |
|---|---|---|
| **Console JWT** (HS256, 12h, aud `tring.console`) | Operators, via `POST /v1/console/login` (email+bcrypt) | `/v1/*` tenant routes (`AuthRequired`) |
| **Agent JWT** (HS256, 5-min, aud `tring.sdk`) | The agent's browser, minted by `GET /v1/agents/{id}/token` | `/v1/sdk/*` (credentials, calls), scoped to the subject agent |
| **Public widget key** (`pk_live_…`) | Anonymous website visitors | `POST /v1/sdk/guest-credentials` only (mints a guest identity) |
| **FS Basic Auth** (`fs:secret`) | FreeSWITCH → API | `/fs/*` callbacks (localhost trust boundary) |

The browser never holds tenant-wide credentials. The agent JWT is minted by the integrator's backend; the public key is embeddable but **bound to allow-listed origins** (see §10).

---

## 7. Outbound call path (agent → customer)

`POST /v1/sdk/calls` (agent JWT) → `internal/freeswitch.Client.Originate` → fresh ESL connection → `bgapi originate <vars>user/<sip>@<domain> <provider B-leg>`:

1. **A-leg** = the agent (`agents.sip_username` + `tenants.sip_domain`); FreeSWITCH rings the agent's registered WebRTC contact.
2. **B-leg** = the trunk, chosen by `internal/provider` + `internal/api/trunks.go::selectTrunks` per the tenant's `outbound_strategy` (`failover` | `round_robin` | `manual` | `load_based`*). Caller-ID stamped per-leg.
3. A `calls` row is written `ringing` with **two** FS identifiers: `fs_uuid` (pre-allocated channel) and `fs_job_uuid` (bgapi job). The orchestrator settles the row by `fs_uuid` on `CHANNEL_*` events, or by `fs_job_uuid` on `BACKGROUND_JOB` when the channel never gets created (e.g. agent not registered → `MANDATORY_IE_MISSING`).

`*load_based` is a reserved enum (returns 400 "not implemented").

---

## 8. Visitor web-calling path (visitor → IVR → agent)

The customer-facing counterpart to the agent dialer. A website visitor with **no account** calls into a tenant's IVR over WebRTC.

```
Customer site (public key)
  └─ POST /v1/sdk/guest-credentials  → ephemeral guest_* SIP identity, pinned to a flow
  └─ register over WSS               → /fs/directory tags user_class=guest, tring_flow_id
  └─ SIP INVITE                      → dialplan `tring_guest_ivr` → IVR executor :8086
        └─ executor resolveFlowByID(tring_flow_id) → walks the flow graph
              └─ Transfer node → bridge to the tenant's agent(s) → agent answers
```

Key properties:
- **Anonymous + flow-pinned:** the guest identity (`internal/api/handlers_guest.go`) is bound to a specific flow; the visitor never chooses a destination.
- **Toll-fraud guard:** the dialplan `tring_guest_ivr` branch matches `user_class=guest` and routes *only* to the IVR socket, regardless of dialed destination — a guest credential can never reach a trunk bridge.
- **Agent answering:** the IVR `Transfer` node rings registered agents (a specific agent via `config.agent_sip`, or all tenant agents simultaneously). The agent widget shows an **incoming-call screen (Accept/Decline)** because it registers with `autoAnswer:false`; only the agent's own *outbound* leg auto-answers.

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

The public mint endpoint (`/v1/sdk/guest-credentials`) is defended in layers:

| Control | Stops |
|---|---|
| **Origin allowlist** (`tenants.allowed_origins`, fail-closed) | Browser-embedding a stolen public key on another site (Origin is unspoofable in a browser) |
| **Per-IP rate limit** (10/min, `internal/api/ratelimit.go`) | Scripted mint floods from one host |
| **Per-tenant concurrent cap** (100 live sessions) | Total capacity exhaustion (non-spoofable backstop) |
| **`user_class=guest` dialplan guard** | **Toll fraud** — guests can never reach a trunk |
| **Ephemeral, flow-pinned creds (15-min TTL)** | Data access, arbitrary dialing |

Net: a stolen key is blocked in a browser; even a scripted spoof can only make **capped, money-safe nuisance calls into the tenant's own IVR**. Operators manage the allowlist + support flow from **Settings → Guest web calling** (`GET/PATCH /v1/guest-config`).

**Production prerequisites** (not enforced by code): valid TLS on the WSS endpoint (FreeSWITCH ships a self-signed cert), a TURN server (ideally ephemeral TURN credentials), and bot mitigation in front of the mint endpoint.

---

## 11. Browser SDK & widgets

`sdk/js/` (TypeScript, esbuild). **The API serves the bundles itself at `/sdk/*`** (embedded via `internal/widget`, same `//go:embed`+`.gitkeep` pattern as the console) so self-hosters need no CDN.

| Bundle | Served | For |
|---|---|---|
| `voice.js` | `/sdk/voice.js` | website visitor → IVR (public key) |
| `agent.js` | `/sdk/agent.js` | CRM agent dialer + incoming calls (agent JWT) |
| `tring.js` | `/sdk/tring.js` | bare SDK (`window.Tring`) |
| `tring.esm.js` | npm | bundler ESM |

The embed snippet derives its origin from the page (no hardcoded host). **Native mobile** is not supported by the JS SDK (browser-only); the path there is a native client (`react-native-webrtc`/`flutter-webrtc` + CallKit/ConnectionService) reusing the **same backend + SIP-over-WS protocol** — see §3.

---

## 12. Trunk provider abstraction

Trunks are **UI-managed** (`/v1/trunks`), rendered to per-trunk gateway XML under `sip_profiles/external/dynamic/` and live-rescanned (zero-downtime). Indian providers (`exotel`, `airtel`, `jio`, `tata`) share `indianSIPTrunk` (`internal/provider/india.go`): same E.164 normalization (`internal/dial`, rejects non-Indian at the boundary), same gateway shape. Adding a provider is a small, contained change; switching a tenant's trunk is config-only.

Outbound combination per call is governed by `tenants.outbound_strategy` (failover / round_robin / manual / load_based), assembled in `originateForAgent` + `selectTrunks`.

---

## 13. Webhooks

`internal/webhook.Notifier` (used by both planes) emits `call.ringing` / `call.answered` / `call.ended`. Per-subscriber `webhook_deliveries` rows; goroutine-per-delivery with retry/backoff (6 attempts). **Stripe-style signing**: `Tring-Signature: t=<unix>,v1=<hmac_sha256(secret, "<t>.<body>")>` + `Tring-Event` + `Tring-Delivery`. Best-effort by design (never blocks the call path). Crash recovery for in-flight deliveries is Phase 2.

---

## 14. Deployment

- **Single `docker compose up -d`** brings up FreeSWITCH + Postgres + api + orchestrator. The api/orchestrator images embed the console + widget bundles (Dockerfile multi-stage: Node builds Vue + SDK → Go embeds → distroless).
- **Config via env** (`.env`, see `.env.example`): DSN, JWT secret, FS basic auth, bootstrap tenant/admin, STUN/TURN, WS URL, public IP / RTP range.
- **Migrations** run automatically on api startup (idempotent). **Bootstrap** seeds a tenant + admin user on an empty DB.
- **FreeSWITCH config** is bind-mounted `:ro` (the base image's entrypoint clobbers RW mounts). It resolves `local_ip_v4` at start, so **a host IP change (e.g. laptop Wi-Fi switch) requires `docker restart tring-fs tring-api`**; static IPs (servers) are unaffected.

---

## 15. Known limitations & roadmap

| Area | Current | Planned |
|---|---|---|
| IVR nodes | greeting/menu/transfer/hangup | gather, http_callback, timing, goto_flow |
| Tenancy | app-level filtering | PostgreSQL RLS |
| Masking pool | in-memory (lost on restart) | mirror to `masking_sessions` |
| Recording | `recording_url` column exists | capture wiring |
| TURN | optional, static creds | required + ephemeral creds for guests |
| Webhooks | in-process goroutines | durable worker (poll pending) |
| Mobile | browser SDK only | native client (CallKit/ConnectionService) |
| Messaging | none | separate SMS plane (Jasmin/provider HTTP, not FreeSWITCH) |
| Call transfer | IVR transfer-to-agent | agent-initiated blind/attended transfer |

---

## 16. Code map (where to look)

| Question | File |
|---|---|
| Outbound call origination | `internal/freeswitch/esl.go`, `internal/api/handlers_calls.go` |
| Inbound call → Go | `cmd/orchestrator/main.go`, `internal/orchestrator/ivr_executor.go` |
| Masking/DID pool | `internal/masking/pool.go` |
| Dynamic SIP directory | `internal/api/handlers_fs.go` |
| Guest calling | `internal/api/handlers_guest.go`, dialplan `tring_guest_ivr` |
| IVR executor / simulator | `internal/orchestrator/{ivr_executor,simulator,sim_session}.go` |
| Trunk selection | `internal/api/trunks.go`, `internal/provider/` |
| Browser SIP wrapper | `sdk/js/src/sip.ts` (over `sip.js`) |
| Admin console | `web/` (Vue 3), embedded via `internal/webui` |
| Widget serving | `internal/widget` (`/sdk/*`) |
| Schema | `internal/migrate/migrations/*.sql` |

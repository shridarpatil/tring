# Tring — Demo Brief & End-to-End Walkthrough

A briefing for showing Tring to a technical audience: the pitch, the architecture,
one clean end-to-end call traced step by step (**with the library used at each
step**), the security model, and a timed live-demo script.

---

## 1. The one-liner

> Tring is an API-first, multi-tenant voice platform — "open-source Twilio Voice,
> India-first." A CRM drops in a `<script>` tag and a button; their agents place
> and receive real phone/web calls. We orchestrate everything through FreeSWITCH,
> but the integrator never sees FreeSWITCH or SIP.

License: Apache-2.0. Self-host or hosted, from the same binaries.

---

## 2. The moving parts

| Component | Role | Exposed? | Scaling |
|---|---|---|---|
| `cmd/api` (:8085) | Public REST API + admin console + FreeSWITCH callbacks | Public | Stateless → horizontal |
| `cmd/orchestrator` (:8084 masking, :8086 IVR) | FreeSWITCH-facing brain: events, IVR/queue/teams, CDRs | Private | Per-FS, vertical |
| `cmd/gateway` (:8087 HTTP, :5090 SIP) | Browser WebRTC ⇄ private FreeSWITCH (SIP B2BUA) | Public (media) | Per-FS, vertical |
| FreeSWITCH (ESL :8021, SIP :5060/:5080) | Media/call engine | **Never internet-exposed** | Per instance |
| Postgres (:5434 dev) | Source of truth | Private | Shared |
| Redis (:6379) | Presence, flow cache, queue depth, single-use tokens | Private | Shared |

**Design rule:** each plane scales differently, so they're separate binaries.
The API is stateless; the orchestrator and gateway are state-aware and pinned per
FreeSWITCH instance.

---

## 3. Library cheat-sheet (what powers each concern)

| Concern | Library |
|---|---|
| Browser ⇄ gateway media (WebRTC, DTLS-SRTP, ICE, TURN) | **pion/webrtc v4** (`pion/dtls`, `pion/srtp`, `pion/ice`, `pion/turn`) |
| Gateway ⇄ FreeSWITCH signaling (SIP B2BUA / UAS) | **emiago/sipgo v1.4** |
| Agent presence + incoming-call signaling (WebSocket) | **gobwas/ws v1.3** |
| FreeSWITCH control (events, originate, IVR socket) | **percipia/eslgo v1.5** |
| All tokens (agent / gateway-call / console JWT) | **golang-jwt/jwt v5** |
| Console password hashing | **golang.org/x/crypto** (bcrypt) |
| HTTP routing (API) | **go-chi/chi v5** |
| Postgres | **jackc/pgx v5** |
| Redis (presence, cache, queue depth, single-use jti) | **redis/go-redis v9** |
| Config (`TRING_*` env) | **knadh/koanf v2** |
| Schema migrations | **golang-migrate v4** |
| Recording storage (S3 / MinIO / R2) | **minio/minio-go v7** |
| Webhook signatures | stdlib `crypto/hmac` (Stripe-style) |
| Browser SDK / widgets | TypeScript + **esbuild**, browser-native `RTCPeerConnection` |
| Admin console | **Vue 3** + Vite + Tailwind + **vue-flow** (flow editor) |

---

## 4. End-to-end: a visitor clicks "Talk to human"

The marquee flow. Each step notes **[lib]**.

```
Browser (visitor)      Gateway            FreeSWITCH        Orchestrator      Agent browser
   │                     │                   │                  │                 │
 1 click                 │                   │                  │                 │
 2 POST /guest-creds ───────────────► API (mint token)         │                 │
 3 POST /webrtc/offer ──►│ (SDP + token)     │                  │                 │
 4                       │ validate token    │                  │                 │
 5                       │◄═ WebRTC peer ═══ (browser)          │                 │
 6                       │ dial "gwflow" ───►│                  │                 │
 7                       │                   │ socket ─────────►│ (IVR executor)  │
 6'│◄════ DTLS-SRTP ════►│◄═════ RTP ═══════►│                  │                 │
 8                       │                   │   walk flow → transfer → team      │
 9                       │◄ SIP INVITE (agent leg) ─────────────┤                 │
10                       │ ring over WS ─────────────────────────────────────────►│
11                       │◄════════════ agent accepts (WebRTC) ════════════════════│
12  visitor ◄═► gateway ◄═► FreeSWITCH ◄═► gateway ◄═► agent   (talking)
13                       │ CHANNEL_BRIDGE → attribute agent + start recording      │
14                       │ hangup → CDR settled, recording → S3, webhooks fired    │
```

1. **Visitor clicks the widget button.** Browser SDK `voice.js`, served by the API
   at `/sdk/voice.js` (embedded via `//go:embed`). **[TypeScript/esbuild]**
2. **CRM backend mints a one-call token** — `POST /v1/guest-credentials` with the
   tenant API key; returns the gateway URL, the pinned flow, and a short-lived
   single-use token. **[chi router + golang-jwt + pgx]**
3. **Browser builds a WebRTC offer and POSTs it** (SDP + token) to the gateway
   `/webrtc/offer`. **[browser-native RTCPeerConnection]**
4. **Gateway validates the token** — signature + audience, and burns the `jti` in
   Redis (`SetNX`, fail-closed so a captured token can't be replayed).
   **[golang-jwt + go-redis]**
5. **Gateway peers WebRTC with the browser** — Opus audio + RFC-4733 DTMF over
   DTLS-SRTP, ICE/TURN for NAT. **[pion/webrtc]**
6. **Gateway dials FreeSWITCH as a SIP B2BUA** to the `gwflow` dialplan extension
   (flow pinned via an `X-Tring-Flow-Id` header) and **bridges browser DTLS-SRTP ↔
   FS plain RTP**, PT-aware. **[sipgo (signaling) + pion (media relay)]**
7. **FreeSWITCH hands the call to the orchestrator** via the dialplan `socket`
   application (outbound ESL on :8086). **[eslgo]**
8. **Orchestrator walks the IVR flow graph** (greeting/menu/…), reaches a transfer
   node → resolves the team, checks agent availability, picks one by strategy.
   **[Go orchestrator + pgx (flow/team) + go-redis (presence/queue depth) + eslgo
   (playback/collect)]**
9. **Bridge to the agent leg** `sofia/gateway/tring-gw/<sip>`; FreeSWITCH sends that
   leg back to the gateway as a SIP INVITE. **[eslgo (bridge) → sipgo (UAS)]**
10. **Gateway rings the agent** over the agent's authenticated WebSocket. **[gobwas/ws]**
11. **Agent accepts** → agent browser offers WebRTC → gateway answers + bridges.
    **[pion/webrtc + browser RTCPeerConnection]**
12. **Both parties are connected** — media path: browser ⇄ gateway ⇄ FS ⇄ gateway ⇄
    browser, all bridged. **[pion + FreeSWITCH RTP]**
13. **On `CHANNEL_BRIDGE`** the orchestrator attributes the answering agent and, if
    the tenant has recording on, starts `uuid_record` (stereo WAV). **[eslgo + pgx]**
14. **On hangup**, the CDR is settled (status/duration), the recording is uploaded
    to S3 (date-wise key) and the local spool deleted, and `call.ended` fires.
    **[pgx + minio-go + crypto/hmac (webhook signing)]**

**Three points that make this design defensible:**
1. The **browser never speaks SIP** and **FreeSWITCH is never internet-exposed** —
   the gateway is the only public media surface (raw WebRTC out, SIP only inward).
2. The **agent leg is identical** for browser-inbound, telco-inbound, and outbound
   click-to-call — all converge on the orchestrator and one `internal/agentleg`
   helper.
3. **Agents are WebRTC endpoints, not SIP phones** — one authenticated WebSocket
   for presence + ringing; no SIP credentials in the browser.

---

## 5. IVR / queue / teams

- Operators build call flows as a **node graph** in the console (greeting, menu,
  gather, http_callback, transfer, hangup), stored as JSONB per tenant. **[vue-flow]**
- A **transfer node** routes to a **team** with a strategy (round-robin /
  sequential / longest-wait). All agents busy → the caller **queues** (priority +
  normal FIFO tiers) with **hold music**; the orchestrator dispatches the head of
  line the instant an agent frees.
- **Concurrency correctness:** when two calls arrive together we **reserve** an
  agent the moment we start ringing them, so two callers can't grab the same idle
  agent (presence only flips to "on call" on *answer*, not *ring*).
- The same graph drives a **pure-Go simulator** so a flow can be tested before going
  live — no FreeSWITCH required.

---

## 6. Security & isolation (they will ask)

Five credentials, never conflated — all JWTs via **golang-jwt/jwt v5**:

| Credential | Who | TTL / notes |
|---|---|---|
| Tenant API key (`tk_live_…`) | CRM backend | Long-lived; full tenant scope; never in a browser |
| Agent JWT (`aud tring.sdk`) | Agent browser | 5-min; authenticates the gateway WS |
| Gateway call token (`aud tring.gateway`) | One visitor call | ~2-min, **single-use** (Redis `SetNX`, fail-closed) |
| Console JWT (`aud tring.console`) | Operator | 12-hr; admin UI |
| FS Basic Auth | FreeSWITCH ↔ API | localhost-only callbacks |

**Tenant isolation:** per-tenant SIP domains in FreeSWITCH; every non-tenant row
carries `tenant_id` and queries filter on it (application-level today; Postgres RLS
is the Phase-2 hardening).

---

## 7. Recording & storage

Per-tenant toggle (Settings → Record calls). On bridge, FreeSWITCH records a stereo
WAV. On completion it's uploaded to **S3** (`recordings/<tenant>/YYYY/MM/DD/<id>.wav`)
and the local spool deleted; playback streams back through the API (no public URLs,
auth via header). S3 auth: static keys **or** the EC2/ECS/EKS **IAM role** (leave
keys empty). Works with AWS S3, MinIO, Cloudflare R2. **[minio-go]**

---

## 8. Scaling & ops

- **API** — stateless → horizontal behind a load balancer.
- **Orchestrator + gateway** — state-aware (in-process call sessions/bridges); one
  set per FreeSWITCH instance, scale vertically per FS, add FS instances to scale out.
- **Postgres + Redis** — shared backing stores.
- **Webhooks** (`call.ringing/answered/ended`) — signed (HMAC), retried with
  backoff, best-effort; never block the call path.

---

## 9. Likely questions — crisp answers

- **Why a custom WebRTC gateway, not SIP-over-WebSocket?** Security + simplicity:
  FreeSWITCH stays private, browsers never get SIP creds or speak SIP, and we get
  clean WebRTC (Opus + DTLS-SRTP) with one hardened public endpoint.
- **Redis or a FreeSWITCH node dies?** Redis: presence/flow-cache degrade gracefully
  — *except* the single-use call token, which fails **closed** (reject over replay).
  A FS node is a vertical unit; losing it drops its in-flight calls but the API/DB
  are unaffected. Cross-node FS HA is roadmap.
- **Multi-tenant isolation?** `tenant_id` everywhere + per-tenant SIP domains +
  scoped tokens; recordings fetched by call-id filtered on tenant. RLS is next.
- **Can real phones call in today?** Outbound + the trunk abstraction
  (Exotel/Airtel/Jio/Tata, config-only) are built. Real *inbound DID* needs two
  boundary pieces (carrier inbound context → IVR socket, DID normalization) wired
  when a live circuit is provisioned — scoped, deliberately deferred.
- **Lock-in / portability?** Apache-2.0, self-host or hosted, S3-compatible storage
  (MinIO/R2, not just AWS), standard Postgres/Redis. FreeSWITCH is the only heavy
  dep and it's open-source.
- **What's not done yet?** Inbound-telco boundary, Postgres RLS, durable webhook
  worker (crash recovery), cross-node FS HA, DLT/recording-consent tooling.

---

## 10. Five-minute live demo script

> Prep: `make up` (all services), one agent created, the agent widget page open and
> showing **online**, and the visitor sample page ready. URLs: console at
> `/admin`, demo pages via `make serve-demo` / `make demo-url`.

**0:00 — Frame it (30s).**
"Tring is API-first voice. A CRM adds a script tag and a button; their agents take
real calls. Everything routes through FreeSWITCH, but the integrator never sees it.
Three Go services around a private FreeSWITCH." *(Show the component table.)*

**0:30 — The console (45s).** Open `/admin`. Show the **Dashboard** (live "On call"
+ "Calls in queue" counters), **Agents** (one agent **online** — presence is from
Redis via the gateway WebSocket), **Flows**. "This whole UI is Vue 3, embedded in
the API binary — no separate frontend to deploy." **[Vue 3 / vue-flow]**

**1:15 — Show a flow (45s).** Open the **Flow editor** — drag-built graph: greeting
→ menu → transfer-to-team. Hit **Test** to run the **simulator** (pure Go, no
FreeSWITCH) and watch it walk the nodes. "Same graph drives the live call and this
simulator, so flows are testable before go-live."

**2:00 — Place the call (90s).** On the visitor page, click **"Talk to human."**
Narrate as it connects:
- "Browser mints a one-call token from the CRM backend, then sends a WebRTC offer to
  our gateway." **[golang-jwt, pion/webrtc]**
- "Gateway validates the single-use token, peers WebRTC, and dials the private
  FreeSWITCH over SIP." **[go-redis, sipgo]**
- "FreeSWITCH hands the call to the orchestrator, which walks the IVR and routes to
  the team." **[eslgo]**
- The **agent widget rings** → accept. "The agent is a WebRTC endpoint on a
  WebSocket — no softphone, no SIP creds in the browser." **[gobwas/ws, pion]**
- Talk both ways to prove live audio.

**3:30 — Show the result (60s).** In the console **Calls** log: the call row appears
with agent, direction, duration, and a **recording player** — hit play (streamed
from S3/local through the API). "Recorded on connect, per-tenant toggle, stored in
S3 with a date-wise key." **[minio-go, pgx]**

**4:30 — The queue (if time, 30s).** With the one agent now busy, place a second
call: it **queues** with hold music; the dashboard "Calls in queue" ticks up. Hang
up the first → the queued caller is **dispatched** to the freed agent. "Native
in-orchestrator queue with priority tiers and an anti-double-ring reservation."

**Close:** "Browser → agent is the proven path you just saw. Inbound from a real
phone number reuses the exact same agent side — only the carrier-facing edge is left
to wire when we provision a trunk."

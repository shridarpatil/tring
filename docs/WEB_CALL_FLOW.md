# Web call flow — authenticated (client-identified) guest calling

How a logged-in web caller (e.g. a support portal that already knows the
client) places a browser call that lands in the IVR, reaches an agent, and
shows up in the call log **as the client** — not as an anonymous `guest_*`
identity.

Since the ADR-001 cutover the browser peers with the **media gateway** over raw
WebRTC; there are no browser SIP credentials and no `guest_sessions` table. The
caller's business identity travels in a short-lived **gateway call token**, not
in a SIP registration. See `docs/ADR-001-webrtc-gateway.md` and
`docs/EDGE_DEPLOYMENT.md`.

## One mint, two policies

There is a **single mint** — `POST /v1/guest-credentials` (tenant **API key**,
`tk_live_…`), called by the integrator's backend (there is no public/browser
mint). It issues a gateway call token; the only difference between an anonymous
guest and a logged-in caller is what the backend checks first and whether it
passes a `client_ref`:

- **Anonymous guest** → the backend gates with a **CAPTCHA / rate-limit**, then
  mints **without** `client_ref`.
- **Logged-in caller** → the backend verifies the session, then mints **with**
  the verified `client_ref` (attributed on the CDR).

`client_ref` is only ever trusted because the request proved tenant ownership
(the API key) — the browser never holds the key and can't set it. This mirrors
the agent widget's `data-token-url` pattern: the integrator's backend issues
call credentials to its own frontend. The minted token is short-lived and
**single-use** (the gateway consumes its `jti` on first `/webrtc/offer`).

## 1. Mint a gateway call token (trust boundary)

```
┌────────────────┐     ┌──────────────────┐     ┌───────────────┐
│ Portal frontend│     │  Portal backend  │     │   Tring API   │
│ (browser,      │     │ (holds tk_live_… │     │  :8085        │
│  logged-in)    │     │  API key)        │     │               │
└──────┬─────────┘     └────────┬─────────┘     └──────┬────────┘
       │ 1. "Call" clicked      │                       │
       │ ── ask for creds ────► │                       │
       │   (your own session)   │                       │
       │                        │ 2. POST /v1/guest-credentials
       │                        │    Authorization: Bearer tk_live_…
       │                        │    { client_ref:"cust_ACME_42", flow_id? }
       │                        │ ─────────────────────►│
       │                        │                        │ 3. AuthRequired ✓ (tenant known)
       │                        │                        │    resolve flow; mintGatewayCall
       │                        │                        │    → sign gateway call token
       │                        │                        │      (aud tring.gateway, ~2-min,
       │                        │ ◄── 200 ───────────────│       flow_id + client_ref TRUSTED)
       │                        │   { gateway_url, flow_id,                │
       │                        │     token, ice_servers }                │
       │ ◄── creds ─────────────│                        │
       │                        │
       ▼ 4. SDK POSTs an SDP offer + token to the gateway /webrtc/offer
            (gateway consumes the token's jti → single-use)

  (Anonymous guests use the SAME endpoint — the backend just gates with a
   CAPTCHA and mints without a client_ref. There is no public/browser mint.)
```

No database row is written at mint time — the token *is* the state, signed and
self-contained, expiring in ~2 minutes.

## 2. The call → IVR → agent → log

```
┌─────────┐   ┌────────────┐   ┌──────────────┐   ┌──────────────────────────┐   ┌──────────┐   ┌───────┐
│ Browser │   │  Gateway   │   │  FreeSWITCH  │   │   Tring orchestrator     │   │ Postgres │   │ Agent │
│  (SDK)  │   │  :8087     │   │  (PRIVATE)   │   │  (:8086 IVR + event loop)│   │          │   │ Alice │
└────┬────┘   └─────┬──────┘   └──────┬───────┘   └────────────┬─────────────┘   └────┬─────┘   └───┬───┘
     │ POST /webrtc/offer            │                          │                     │             │
     │  (SDP + gateway call token)   │                          │                     │             │
     │ ────────────►│                │                          │                     │             │
     │              │ validate token: flow_id + client_ref      │                     │             │
     │              │ (gateway/auth.go) — browser never set them │                    │             │
     │              │ dial FS gwflow ext, INVITE carries         │                     │             │
     │              │  X-Tring-Flow-Id + X-Tring-Client-Ref ───►│                     │             │
     │              │                │ FS maps headers → channel vars              │             │
     │              │                │  tring_flow_id, tring_client_ref=cust_ACME_42 │             │
     │              │                │ ── socket :8086 ────────►│ HandleInboundCall   │             │
     │              │                │                          │  reads client_ref   │             │
     │              │                │                          │ ── INSERT calls ───►│ direction=inbound
     │              │                │                          │   from=client_ref,  │ from_number, client_ref,
     │              │                │                          │   status=ringing    │ fs_uuid
     │ ◄── SDP answer ──────────────│ ◄── answer + walk flow ───│                     │             │
     │  bridge browser DTLS-SRTP ⇄ FS RTP                        │                     │             │
     │  greeting / menu (DTMF) ... transfer node                 │ record IVR steps    │             │
     │              │                │ transfer → agent leg via gateway (tring-gw)     │             │
     │              │                │ ────────────────────────────────────────────────────────────►│ RING
     │              │                │ ◄────────────────── CHANNEL_BRIDGE (A=call, B=alice) ──────────│ ANSWER
     │              │                │                          │ onBridge: stamp agent_id            │
     │              │                │                          │ ── UPDATE calls ───►│ agent_id=Alice
     │   ... talk ...                │                          │                     │             │
     │              │                │ ◄── CHANNEL_HANGUP_COMPLETE ─│ onHangup: settle │ status=ended,
     │              │                │                          │ ── UPDATE calls ───►│ duration, cause
     │              │                │                          │ defer: persist ─────►│ ivr_path = [start,
     │              │                │                          │   IVR journey        │  greeting, menu:1,
     │              │                │                          │                     │  transfer→Alice(answered),
     ▼              ▼                ▼                          ▼                     ▼  hangup]
                                                Console /admin/calls reads it back:
                       From          Agent   Dir      Status  IVR path
                       cust_ACME_42  Alice   inbound  ended   ▸ view (6)  ← no "guest_…"
```

## Trust in one line

`client_ref` is only ever written into the token by the **API-key** mint (steps
2–3) — the browser never supplies it — so the From column shows a server-asserted
client identity, not a spoofable label. Everything downstream (token claim →
`X-Tring-Client-Ref` SIP header → `tring_client_ref` channel var →
`calls.client_ref` → call log) just carries that trusted value along.

## Where it lives in the code

| Step | File |
|------|------|
| Backend mint (optional trusted `client_ref`) | `internal/api/handlers_guest.go` — `handleAuthGuestCredentials` → `mintGatewayCall` |
| Gateway call token issue/parse (with `jti`)  | `internal/api/jwt.go` — `IssueGatewayCallToken` / `ParseGatewayCallToken` (`GatewayCallClaims`) |
| Route registration                          | `internal/api/server.go` — `/v1/guest-credentials` (AuthRequired) |
| Token validation + single-use at the edge   | `internal/gateway/auth.go` — `validateGatewayCallToken`; `internal/gateway/server.go` — `handleOffer` (jti consume) |
| `client_ref` → SIP header → CDR           | `internal/gateway/fsleg.go` — `X-Tring-Client-Ref`; `internal/orchestrator/ivr_executor.go` — `HandleInboundCall` records it |
| IVR path recording                        | `internal/orchestrator/ivr_executor.go` — `walk` / `recordStep`, persisted via `saveIVRPath` |
| Agent attribution on answer               | `internal/orchestrator/runner.go` — `onBridge` (CHANNEL_BRIDGE) stamps `calls.agent_id` |
| Call-log read + display                   | `internal/api/handlers_calls.go` (`Call`/`scanCall`); `web/src/views/CallsView.vue` |

## Integration note

Point the visitor SDK's `data-credentials-url` at **your** backend, which returns
what it obtained from `POST /v1/guest-credentials` (gateway URL + flow + the
client-bearing token). For anonymous "call us", the SDK can instead go straight
to the gateway with `data-gateway-url` + `data-flow-id` (no token; flow-pinned,
can never reach a trunk). See `examples/demo-portal/` — one app with the
`/portal` (trusted), `/support` (anonymous), and `/crm` (agent) flows.

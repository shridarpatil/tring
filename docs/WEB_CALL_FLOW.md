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

## Why two mint paths

The caller's *business* identity (`client_ref`) can only be trusted when the
request proves tenant ownership, so the API exposes two mints — both issue a
gateway call token, they differ only in whether `client_ref` is honoured:

- **Public mint** — `POST /v1/sdk/guest-credentials` (Origin allowlist + per-IP
  rate limit, no auth). For anonymous website visitors. **`client_ref` is
  ignored here** — a browser-supplied identity would be spoofable. Can be
  disabled per-tenant (then this returns 403 and you must mint server-side).
- **Authenticated mint** — `POST /v1/guest-credentials` (tenant **API key**,
  `tk_live_…`). For the integrator's backend, which knows the verified
  logged-in client. **`client_ref` is trusted** and baked into the token.

Both resolve the flow and call the shared `mintGatewayCall`. The browser never
holds the API key — this mirrors the agent widget's `data-token-url` pattern:
the integrator's backend issues call credentials to its own authenticated
frontend.

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

  ✗ Public  POST /v1/sdk/guest-credentials  (Origin allowlist + rate limit, no auth)
            → mints a token but client_ref is IGNORED (browser can't forge identity)
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
| Authenticated mint (trusted `client_ref`) | `internal/api/handlers_guest.go` — `handleAuthGuestCredentials` → `mintGatewayCall` |
| Public mint (ignores `client_ref`)        | `internal/api/handlers_guest.go` — `handleGuestCredentials` → `mintGatewayCall` |
| Gateway call token issue/parse            | `internal/api/jwt.go` — `IssueGatewayCallToken` / `ParseGatewayCallToken` (`GatewayCallClaims`) |
| Route registration                        | `internal/api/server.go` — `/v1/guest-credentials` (AuthRequired) + `/sdk/guest-credentials` (public) |
| Token validation at the edge              | `internal/gateway/auth.go` — `validateGatewayCallToken`; `internal/gateway/server.go` — `handleOffer` |
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

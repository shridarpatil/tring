# Web call flow — authenticated (client-identified) guest calling

How a logged-in web caller (e.g. a support portal that already knows the
client) places a browser call that lands in the IVR, reaches an agent, and
shows up in the call log **as the client** — not as an anonymous `guest_*`
identity.

## Why two mint paths

Web callers register with an ephemeral `guest_*` SIP identity minted on demand.
The caller's *business* identity (`client_ref`) can only be trusted when the
request proves tenant ownership, so there are two mints:

- **Public mint** — `POST /v1/sdk/guest-credentials` (tenant *public* key, Origin
  allowlist, rate limit). For anonymous website visitors. **`client_ref` is
  ignored here** — a browser-supplied identity would be spoofable.
- **Authenticated mint** — `POST /v1/guest-credentials` (tenant **API key**,
  `tk_live_…`). For the integrator's backend, which knows the verified
  logged-in client. **`client_ref` is trusted** and attributed on the call log.

The browser never holds the API key. This mirrors the agent widget's
`data-token-url` pattern: the integrator's backend issues call credentials to
its own authenticated frontend.

## 1. Mint credentials (trust boundary)

```
┌────────────────┐     ┌──────────────────┐     ┌───────────────┐     ┌──────────┐
│ Portal frontend│     │  Portal backend  │     │   Tring API   │     │ Postgres │
│ (browser,      │     │ (holds tk_live_… │     │  :8085        │     │          │
│  logged-in)    │     │  API key)        │     │               │     │          │
└──────┬─────────┘     └────────┬─────────┘     └──────┬────────┘     └────┬─────┘
       │ 1. "Call" clicked      │                       │                   │
       │ ── ask for creds ────► │                       │                   │
       │   (your own session)   │                       │                   │
       │                        │ 2. POST /v1/guest-credentials             │
       │                        │    Authorization: Bearer tk_live_…        │
       │                        │    { client_ref:"cust_ACME_42",flow_id? } │
       │                        │ ─────────────────────►│                   │
       │                        │                        │ 3. AuthRequired ✓ │
       │                        │                        │    (tenant known) │
       │                        │                        │ ── INSERT ──────► │ guest_sessions
       │                        │                        │   guest_xxx +     │  (sip creds,
       │                        │                        │   client_ref      │   client_ref,
       │                        │                        │   (TRUSTED)       │   flow, TTL)
       │                        │ ◄── 200 {sip_user,pass,│                   │
       │                        │      sip_domain,ws_url}│                   │
       │ ◄── creds ─────────────│                        │                   │
       │                        │                        │                   │
       ▼ 4. SDK registers + places call with those SIP creds

  ✗ Public  POST /v1/sdk/guest-credentials  (public key, no auth)
            → mints guest_xxx but client_ref is IGNORED (browser can't forge identity)
```

## 2. The call → IVR → agent → log

```
┌─────────┐   ┌──────────────┐   ┌──────────────────────────┐   ┌──────────┐   ┌───────┐
│ Browser │   │  FreeSWITCH  │   │   Tring orchestrator     │   │ Postgres │   │ Agent │
│  (SDK)  │   │              │   │  (:8086 IVR + event loop)│   │          │   │ Alice │
└────┬────┘   └──────┬───────┘   └────────────┬─────────────┘   └────┬─────┘   └───┬───┘
     │ INVITE (guest_xxx)                       │                     │             │
     │ ──────────►│                             │                     │             │
     │            │ auth lookup → POST /fs/directory (mod_xml_curl)   │             │
     │            │   returns vars: tring_flow_id, tring_guest_session,│            │
     │            │                 tring_client_ref = cust_ACME_42 ◄──────────────│ (from
     │            │                             │                     │             │  guest_sessions)
     │            │ ── socket :8086 ───────────►│ HandleInboundCall   │             │
     │            │                             │  reads client_ref   │             │
     │            │                             │ ── INSERT calls ───►│ direction=inbound
     │            │                             │   from=client_ref,  │ from_number, client_ref,
     │            │                             │   status=ringing    │ fs_uuid
     │            │ ◄── answer + walk flow ──────│                     │             │
     │  greeting / menu (DTMF) ... transfer node │ record IVR steps    │             │
     │            │                             │                     │             │
     │            │ bridge [tring_agent=alice]user/alice@…             │             │
     │            │ ───────────────────────────────────────────────────────────────►│ RING
     │            │ ◄────────────────── CHANNEL_BRIDGE (A=call, B=alice) ─────────────│ ANSWER
     │            │                             │ onBridge: read tring_agent off B    │
     │            │                             │ ── UPDATE calls ───►│ agent_id=Alice
     │   ... talk ...                            │                     │             │
     │            │ ◄── CHANNEL_HANGUP_COMPLETE ─│ onHangup: settle    │ status=ended,
     │            │                             │ ── UPDATE calls ───►│ duration, cause
     │            │                             │ defer: persist ─────►│ ivr_path = [start,
     │            │                             │   IVR journey        │  greeting, menu:1,
     │            │                             │                     │  transfer→Alice(answered),
     │            │                             │                     │  hangup]
     ▼            ▼                             ▼                     ▼
                                       Console /admin/calls reads it back:
                       From          Agent   Dir      Status  IVR path
                       cust_ACME_42  Alice   inbound  ended   ▸ view (6)  ← no "guest_…"
```

## Trust in one line

`client_ref` is only ever written by the **API-key** mint (steps 2–3) — the
browser never supplies it — so the From column shows a server-asserted client
identity, not a spoofable label. Everything downstream (`tring_client_ref`
channel variable → `calls.client_ref` → call log) just carries that trusted
value along.

## Where it lives in the code

| Step | File |
|------|------|
| Authenticated mint (trusted `client_ref`) | `internal/api/handlers_guest.go` — `handleAuthGuestCredentials` → `mintGuestSession` |
| Public mint (ignores `client_ref`)        | `internal/api/handlers_guest.go` — `handleGuestCredentials` |
| Route registration                        | `internal/api/server.go` — `/v1/guest-credentials` (AuthRequired) |
| Session storage                           | `queries.sql` — `create-guest-session`, `get-guest-session`; `guest_sessions.client_ref` (migration `0005`) |
| Directory channel variable                | `internal/api/handlers_fs.go` — `writeGuestDirectory` emits `tring_client_ref` |
| Call-row write                            | `internal/orchestrator/ivr_executor.go` — `HandleInboundCall` → `recordInboundCall` |
| IVR path recording                        | `internal/orchestrator/ivr_executor.go` — `walk` / `recordStep`, persisted via `saveIVRPath` |
| Agent attribution on answer               | `internal/orchestrator/runner.go` — `onBridge` (CHANNEL_BRIDGE) stamps `calls.agent_id` |
| Call-log read + display                   | `internal/api/handlers_calls.go` (`Call`/`scanCall`); `web/src/views/CallsView.vue` |

## Integration note

The visitor SDK currently fetches credentials from Tring's public endpoint
directly. For this trusted flow, point it at **your** backend instead (return
the creds your backend obtained from `POST /v1/guest-credentials`). A
"bring-your-own-credentials" option on the visitor SDK is a small follow-up if
that wiring should be built in.

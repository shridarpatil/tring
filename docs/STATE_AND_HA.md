# Tring — State & High-Availability Posture

**Status:** design / decision doc. Defines what state Tring holds, what survives a
restart, how we scale, and how we deploy without dropping the world. Companion to
[`ARCHITECTURE.md`](ARCHITECTURE.md).

---

## 1. Why this doc exists

Tring's gateway and orchestrator hold **in-process call state** (SFU rooms,
PeerConnections, ESL channels, the masking pool). That has two consequences we
must design around deliberately, not discover in an incident:

- A process restart **drops the calls that process is anchoring**.
- You **cannot** put N gateways behind a round-robin LB and expect calls to work —
  a call's media lives in exactly one process.

This doc commits to a posture for both.

## 2. The core constraint (read this first)

**Media state is process-bound and cannot be migrated.** A live WebRTC
`PeerConnection`, an RTP socket, a DTLS-SRTP session, an SFU fan-out track, or a
FreeSWITCH channel **cannot be moved to another process** — they're bound to the
OS sockets and crypto state of the process that created them. This is true of
*every* media server (mediasoup, Janus, FreeSWITCH itself), not a Tring choice.

Therefore **"make active calls survive a restart" is not an achievable goal** and
we will stop treating it as one. The achievable goals are:

1. **Contain the blast radius** — one node's restart affects only *its* calls.
2. **Drain gracefully** — a planned deploy finishes or sheds calls cleanly, it
   doesn't guillotine them mid-sentence.
3. **Recover NEW-call capacity fast** — a restarted/replacement node serves new
   calls within seconds, with correct system state.
4. **Leave no orphans** — a dropped call's CDR is *settled*, never stuck
   `answered` forever; presence/locks self-heal.

Reconnect ≠ restart: a *transient socket drop with the page still alive* IS
recoverable (the room grace window + `sessionToken` reattach, already built). A
*process restart* or *full page navigation* is not — the media is gone. We keep
the grace window for the former and design the posture around the latter.

## 3. State inventory

State falls into three classes. The design hinges on classifying each correctly.

### Class A — Media / session state (process-bound, NOT relocatable)
| State | Where |
|---|---|
| SFU rooms, publications, fan-out tracks, ports | gateway `internal/media/sfu` |
| PeerConnections, RTP sockets, DTLS sessions | gateway (pion) |
| Room participants + grace timers | gateway `internal/media/room` |
| Inbound ESL connection + per-call channels | orchestrator ↔ FreeSWITCH |
| FreeSWITCH channels / bridges | FreeSWITCH |

→ **Dies with the process. Not externalizable. Drain, don't migrate.**

### Class B — Coordination / lookup state (SHOULD be externalized)
| State | Where today | Durable? |
|---|---|---|
| Agent presence (online/on_call) | Redis (TTL keys) | ✅ already Redis |
| Single-use token jti | Redis `SetNX` | ✅ already Redis |
| Queue depth | Redis (orchestrator writes) | ✅ ephemeral, fine |
| Masking pool (DID ↔ party map) | **orchestrator in-memory** | ❌ wipes on restart (schema `masking_sessions` exists, unused) |
| CDR "in progress" (which calls are live) | gateway `callcdr` in-memory + `calls.status='answered'` | ⚠️ row is durable but no liveness link |
| on_call counter, pending rings | gateway in-memory | ❌ (derivable; acceptable) |

→ **The consistency-critical ones (presence, masking, CDR liveness) must live in
Redis/Postgres so the system view is correct across restarts and nodes.**

### Class C — Durable record (already correct)
Postgres (`calls`/CDR, tenants, agents, flows, numbers), finished recordings
(local volume → S3). These survive everything. No change.

## 4. Failure modes today

| Event | Result today | Acceptable? |
|---|---|---|
| Transient WS drop, page alive | reconnect via grace + reattach | ✅ handled |
| Gateway restart/crash | all its calls drop; **CDRs stuck `answered`**; presence self-heals (TTL) | ⚠️ orphaned CDRs |
| Orchestrator restart | FS-path calls drop; **masking pool lost** (active masked calls misroute) | ❌ masking loss |
| Deploy (`docker compose up`) | hard kill mid-call | ❌ no drain |
| Add a 2nd gateway | calls/agents split across processes with no affinity → broken | ❌ no routing story |

## 5. The posture (decisions)

### P1 — Anchored calls, graceful drain. Never migrate media.
A call is anchored to one node for its lifetime. We do **not** build live media
migration. Availability comes from draining + fast capacity recovery, not from
keeping individual calls alive across a restart.

### P2 — Scale in cells (shared-nothing media), not by sharing call state.
The scaling unit is a **cell**: one co-located `{FreeSWITCH + orchestrator +
gateway}`. A call lives entirely within one cell. Cells share **only** Postgres +
Redis (coordination + records), never call/media state. Scale by adding cells; a
**router** assigns each new call to a cell and pins all of that call's legs +
sockets to it.

Rationale: this sidesteps the impossible problem (moving media between nodes) and
matches the existing reality (gateway/orchestrator already bind per-FreeSWITCH).
It's the standard shape for stateful real-time systems.

### P3 — Externalize Class-B state.
- **Masking pool → mirror to Postgres** (`masking_sessions`, schema already
  provisioned). In-memory stays the hot path; Postgres is the recovery store —
  load on orchestrator boot. *(This is the one Class-B gap with real call-routing
  impact.)*
- **CDR liveness** → a restarted/peer node, and a reconciliation sweep, must be
  able to tell a live call from an orphaned row (see P5).
- Presence, jti, queue depth: already external. Keep.

### P4 — Graceful drain on deploy (SIGTERM).
On shutdown signal each node:
1. Fails its **readiness** probe → the router stops sending it new calls.
2. Keeps serving existing calls until they end **or** a `drain_timeout` (e.g.
   60–120s) elapses.
3. On timeout, sheds remaining calls *cleanly* — sends each leg a terminal
   `ended`, settles its CDR — then exits.
Rolling deploys then move new traffic to new nodes while old calls finish on old
nodes. No mid-call guillotine.

### P5 — Reconciliation sweeper for orphans.
A periodic job (in the API, the system-of-record service) settles state that a
dead node left behind:
- `calls` rows `answered`/`ringing` with **no live anchor** and `started_at`
  older than `max_call_seconds` → mark `ended` (cause `node_lost`) + finalize.
- Liveness signal options: a per-call heartbeat key in Redis written by the
  anchoring node (TTL ~30s), absence = dead; or a node-id stamped on the call +
  a node-liveness key. The sweeper trusts the absence of the heartbeat.
This guarantees **no CDR is stuck `answered`** and the dashboard on-call count is
eventually correct even after an ungraceful crash.

### P6 — Routing & affinity rules.
- **Affinity key = call/room id.** Every leg of a call (visitor offer, agent
  answer, the phone leg) and the related sockets must reach the **same gateway**.
- **Agent homing:** an agent's browser connects to **one** gateway (their cell);
  presence in Redis is global (any cell can *see* who's online) but *ringing*
  them routes the call into their cell. Cross-cell media bridging is a non-goal
  for v1 (see §7).
- Mechanism: sticky routing at the edge (consistent-hash on call id, or the
  guest-credentials/agent-token mint returns a cell-pinned gateway URL — the mint
  already returns `gateway_url`, so it's the natural place to pin a cell).

## 6. What to build (mapped to current code)

| Item | Where | Effort |
|---|---|---|
| Masking pool ↔ Postgres mirror + boot reload | `internal/masking`, `masking_sessions` table | M |
| Per-call liveness heartbeat (node writes, sweeper reads) | gateway `callcdr` + orchestrator runner → Redis | M |
| Reconciliation sweeper | new job in `cmd/api` (system of record) | S |
| Graceful drain (SIGTERM → readiness false → drain → shed) | `cmd/gateway`, `cmd/orchestrator` main + signaling/runner | M |
| Readiness vs liveness probes split | all three binaries (`/readyz` vs `/healthz`) | S |
| Cell-pinned gateway URL in the mints | `internal/api` guest-credentials / agent token | M |
| Edge router / cell selection | new (only when >1 cell is real) | L — defer |

## 7. Non-goals (explicit)

- **Live call migration** between nodes. Impossible for media; we don't attempt it.
- **Cross-cell media bridging** (a call in cell A talking to an agent homed in
  cell B). Deferred — single-cell vertical scale covers near-term load; design the
  router so it's *possible* later, don't build it now.
- **Surviving a process restart with the call intact.** Not a goal (§2).
- **HA of FreeSWITCH itself** (carrier-grade SIP redundancy). Separate concern;
  the plug boundary (`room.PstnGateway`) lets us swap in a redundant trunk stack
  later without touching call logic.

## 8. Phasing

1. **Correctness first (single cell):** liveness heartbeat + reconciliation
   sweeper (P5) + masking durability (P3). Closes the "stuck CDR / lost masking"
   gaps with zero scaling work. *This is the high-value, low-cost start.*
2. **Operability:** graceful drain + readiness probes (P4). Makes deploys
   non-disruptive on a single cell.
3. **Scale-out:** cell-pinned mints + edge router (P2, P6). Only when one cell's
   ceiling (≈150–300 concurrent calls, per `ARCHITECTURE.md` §12) is in sight.

## 9. Open questions

- **Agent homing across cells.** If agents and inbound calls land in different
  cells, ringing requires either cross-cell bridging (non-goal) or routing the
  call to the agent's cell. Acceptable while single-cell; needs a decision before
  multi-cell. Likely answer: route by *agent/team availability* at call setup so
  the call is born in a cell that can serve it.
- **Heartbeat store = Redis.** Redis is already a hard dependency when gateway
  auth is on; using it for call liveness is consistent, but it makes Redis
  load-bearing for CDR correctness too. Acceptable, but note the dependency.
- **Drain timeout vs `max_call_seconds`.** A drain that waits for the longest
  call could block a deploy for the tenant's max call duration. Cap drain at a
  fixed window and shed the rest (P4 step 3).

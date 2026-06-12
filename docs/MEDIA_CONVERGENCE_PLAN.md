# Media convergence plan (Option 2 — locked)

Target: one IVR/routing **brain** (orchestrator), one media **anchor** (pion), every
call leg a `ParticipantSource`. FreeSWITCH becomes the PSTN edge only. The pion
media plane is a thin **public edge**; the orchestrator stays **private**.

```
        PUBLIC / untrusted edge            │   PRIVATE core (the brain)
  browser ──/rtc──┐                        │
  WhatsApp ─API──┐ │                        │
  phone ──SIP──┐ │ │                        │
               ▼ ▼ ▼                        │
        ┌──────────────┐   control channel  │  ┌────────────────────────┐
        │  pion SFU     │◀──(events up)──────┼─▶│ orchestrator = BRAIN    │
        │  (media only) │──(commands down)───┼──│ ivr.Walk · queue · team │
        │  + TrackSinks │                    │  │ routing · presence · CDR │
        └──────────────┘                    │  │ recording owner          │
   no DB · no FS-ctl · no routing            │  └──────────┬──────────────┘
                                           │             │ ESL (private)
                                           │      FreeSWITCH = PSTN edge only
```

## Invariants (the "don't drift" rules)
1. **No business logic on the edge.** The pion edge moves media + exposes hooks
   (inject prompt, DTMF event, add/remove participant, start/stop record). No DB,
   no FS control, no routing/IVR logic. (This undoes the current `ivrdrive` /
   `flowstore` / ring living in the gateway.)
2. **One IVR walker** — `internal/ivr` stays the single brain; never fork it.
3. **Every leg is a `ParticipantSource`** — browser, WhatsApp, transcoded phone
   leg all inject into a pion room; the room can't tell them apart (M6).
4. **Edge = public/DMZ, core = private.** The orchestrator is never internet-exposed.
5. **FreeSWITCH is PSTN-only** — a future `PstnGateway` adapter, never a 2nd brain.
6. **One recorder, one CDR** — recording is a single `TrackSink` for all call types;
   the orchestrator owns the file + CDR reference.

## Phases
- **Phase 1 — Universal recorder.** A `TrackSink`: per-participant Opus RTP →
  Ogg (lossless, no transcode), stereo finalize (agent L / customer R). Runs where
  the SFU is; orchestrator owns the CDR field. Deletes nothing. *In progress.*
- **Phase 1.5 — `/rtc` SDK.** Promote `rtc-test.html` / `rtc-agent.html` logic into
  `sdk/js` (`rtc.ts` visitor + `rtc-agent.ts`). **Freeze the `/rtc` message schema as
  the public client contract** so the Phase 2 server change is invisible to integrators.
- **Phase 2 — Brain↔edge control channel + move web IVR into orchestrator.** Private
  control protocol (events up: join+flow+tenant+client_ref, DTMF, leave, agent
  online/offline; commands down: run/stop IVR voice, play prompt, add/remove
  participant, start/stop record). Orchestrator runs `ivr.Walk` for web with a remote
  pion driver, reusing its existing queue/team/presence/routing. **Delete** the
  gateway's `ivrdrive`, `flowstore`, `agents.go` ring, dev flow.
- **Phase 3 — WhatsApp adapter.** Business Calling API webhook → inject the customer's
  Opus leg as a `ParticipantSource`; orchestrator runs the call. Reference: `../whatomate`.
- **Phase 4 — FreeSWITCH as `PstnGateway`.** SIP/G.711 → Opus → `ParticipantSource`.
  Phone runs on the pion anchor; retire FS-side IVR / `eslDriver`. Biggest/riskiest, last.
- **Phase 5 (opt) — Service consolidation.** Merge `api`+`orchestrator` into one **core**
  binary; the **edge** stays separate (security). Net: 2 Go services + FS + PG + Redis.

## SDK note
Option 2 does NOT change the browser-facing `/rtc` protocol — it's a server-internal
move. The currently *published* SDKs (`gateway.ts` / `agent-gateway.ts`) speak the OLD
FreeSWITCH-bridge protocol and are already obsolete since the M5 pion cutover, so a
`/rtc` SDK is needed regardless of Option 1/2. Build it once (Phase 1.5), freeze the
contract, and the convergence never touches the client again.

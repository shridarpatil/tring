# Supervision (listen / whisper / barge) for phone calls

Date: 2026-10-22
Status: implemented

## Goal

Console supervisors can monitor, whisper and barge on **every live call that has
a media room** — web, phone and WhatsApp — not just web calls. Today the API and
the dashboard both refuse anything but `source = 'web'`.

## Scope

In scope: calls whose `calls.room_id` is set, i.e. calls whose media flows
through the gateway SFU. That covers inbound phone calls (`pstn.go`), outbound
calls placed from the agent web client (`outbound.go`), IVR dial-outs
(`ivrdial.go`), and WhatsApp legs — all of which enter the room as a
bidirectional **port** (`sfu.AttachPort`).

Out of scope:

- Calls originated via `POST /calls` over ESL (`handlers_calls.go`). They have
  no pion room (`room_id` is NULL), so there is nothing to join; supervising
  them would need FreeSWITCH `mod_eavesdrop`, a separate mechanism. The API
  keeps returning 409 "call has no media room" for them.
- More than one simultaneous supervisor per call (unchanged from today).
- Whisper to an agent who is themselves on a port (e.g. reached on a mobile
  via IVR dial-out). Whisper targets agent-role WebRTC participants only, as
  today.

## Background: why phone calls are blocked today

1. `internal/api/handlers_supervise.go` rejects `source != "web"` with 409.
2. `web/src/views/DashboardView.vue` renders supervisor controls only for
   `c.source === 'web'`.
3. **Ports ignore audience.** A port subscribes itself as a sink on every
   publication (`forward.go` onPublish, `inject.go` InjectSource,
   `port.go` AttachPort) without consulting `sfuRoom.audience`. WebRTC
   subscribers are gated by `canHear` in `publicationsExcept`; ports are not.
   So a phone caller can hear a supervisor in monitor/whisper mode. The
   web-only gate is what currently prevents that leak — it cannot be removed
   on its own.
4. **Ports forward one publisher at a time** (`roomPort.WriteRTP`, the
   `active` feed). The outbound stream to FreeSWITCH is a single Opus RTP flow
   with no mixing, so in barge the caller would hear the agent *or* the
   supervisor, never both.

Delivery is two phases. Phase 1 fixes (1)–(3) and gives monitor + whisper on
phone calls. Phase 2 fixes (4) and gives barge.

## Phase 1 — monitor and whisper on phone calls

### SFU: ports honor publisher audience

- Add `publication.removeSink(sink)` (CAS-swap, mirror of `addSink`).
- Every place a port is attached as a sink on a publication — AttachPort
  (existing pubs), onPublish (fresh pub), InjectSource — attaches it only if
  `r.audienceHears(publisher, port.pid)`. Use `audienceHears`, **not**
  `canHear`: the subscriber-side `deaf` map is for the holding web agent and
  must not change port behavior.
- `SetAudience` re-wires ports as well as peers: `setAudienceState` also
  returns, per room port, whether it should hear `pid`; `SetAudience` then
  calls `pub.addSink(port)` / `pub.removeSink(port)`. Adding must be
  idempotent (don't attach a port twice).
- On removing a port's sink, call `port.clearActive(pid)` so a supervisor
  that held the active feed releases it immediately.

Net effect: monitor → caller hears nothing from the supervisor; whisper →
only agent-role WebRTC legs hear the supervisor; barge → `all = true`, the
port is attached (but still single-feed until Phase 2).

### API

- `handleSupervise`: drop the `source != "web"` check. The remaining guards
  (live status, non-empty `room_id`) already express "has a media room".
- Update the handler's doc comment ("live web call" → "live call with a media
  room").

### Gateway liveness check

`handleSupervisor` treats a room as live when `r.Participants()` is non-empty.
A PSTN caller is attached only as an SFU port (`pstn.go` calls `AttachPort`,
not `rooms.Join`), so a ringing phone call with no agent yet has zero
participants. Treat the room as live when it has participants **or**
`s.sfu.HasPort(roomID)`, and ensure the room-ended path that calls
`closeSupervisors` also fires when the last port detaches.

### Web UI

- `DashboardView.vue`: show supervisor controls when the call is live and has
  a room (`c.room_id`, already serialized by `internal/callrow` as
  `room_id,omitempty`), instead of `c.source === 'web'`.
- Until Phase 2 ships, hide/disable **Barge** for non-web calls with a tooltip
  ("barge on phone calls coming soon"), so nobody gets the half-working
  single-feed behavior.

### Tests (Phase 1)

- `sfu/audience_test.go` / `audience_e2e_test.go`: with a port in the room, a
  supervisor in monitor or whisper produces **zero** packets on the port's
  `write`; switching to barge attaches it; switching back detaches it.
- Order variants: supervisor publishes before vs. after the port attaches;
  audience set before vs. after publish.
- `sfu/port_test.go`: `removeSink` of the active feed releases it to the next
  publisher.
- `api/handlers_supervise_test.go`: a live `phone` call with a room returns
  200; a phone call with NULL `room_id` returns 409.

## Phase 2 — barge on phone calls (Opus mixing in the port)

### Design

`roomPort` gains a second mode:

- **Passthrough** (default, today's behavior): forward one active publisher,
  restamped. No decode, no CPU cost.
- **Mix**: entered while the port can hear more than one *talking* source
  that should be mixed — concretely, while a barge supervisor is attached to
  the port. Each audible publisher's Opus packets are decoded into a per-source
  20 ms PCM frame slot; a 20 ms ticker sums the latest frame of each source
  (saturating int16 add), Opus-encodes the result, and writes it out on the
  port's existing continuous seq/ts/SSRC, so FreeSWITCH sees one unbroken
  stream across mode switches. Sources with no fresh frame in a tick
  contribute silence (no stale replay).
- Switching back to passthrough when the barge ends resets decoder state and
  resumes forwarding the agent's packets.

The SFU decides the mode: `SetAudience` with `all = true` for a supervisor
pid that has a port sink → `port.setMix(true)`; leaving barge or the
supervisor leaving → `setMix(false)`.

Codec parameters: decode/encode 48 kHz, mono output (the phone leg is narrow
after FreeSWITCH anyway), 20 ms frames, matching `opusFrameSamplesPort`.
Use the publisher's payload type as today.

### Dependency and build

Opus encode/decode needs libopus via cgo: `gopkg.in/hraban/opus.v2`.

- **New external dependency** — check it (and libopus/libopusfile versions)
  against the approved-dependency list before adding.
- The current Dockerfile builds every binary with `CGO_ENABLED=0`, cross-
  compiled via `--platform=$BUILDPLATFORM` on alpine, into
  `distroless/cc-debian12`. The gateway must instead build with
  `CGO_ENABLED=1` against libopus, either:
  - natively per target arch on a Debian builder, copying `libopus.so.0`
    into the distroless image, or
  - statically linking libopus (alpine builder + `-extldflags -static`).
  Pick during planning; keep the other binaries CGO-free.
- Keep the mixer behind a build tag or small interface (`portMixer`) so unit
  tests of passthrough don't need libopus, and so a pure-Go Opus encoder can
  replace it later.

### Tests (Phase 2)

- Mixer unit test: two sine sources at different frequencies in → decoded
  output contains both (FFT/energy at both frequencies).
- Port mode test: passthrough → mix → passthrough keeps seq/ts strictly
  monotonic with the 960-sample step and a single SSRC.
- E2E: phone port + agent + barge supervisor → port output energy present
  when either the agent or the supervisor talks.
- CPU sanity: mixing cost per barged call measured and noted.

### Web UI

Remove the Phase 1 barge restriction for non-web calls.

## Risks

- Phase 1 changes sink wiring that the IVR and hold music also use. Those are
  ordinary publishers with no audience entry (`nil` → everyone hears), so
  behavior is unchanged; the tests above must keep the existing IVR/hold port
  tests green.
- Phase 2 adds CPU per barged phone call (decode N + encode 1 every 20 ms)
  and a cgo/build-pipeline change for the gateway image.

# Inbound SIP plug end-to-end test (gated, SIPp)

This is a **gated** end-to-end test for the FreeSWITCH inbound SIP plug
(`internal/fsplug`). Unlike `sim/sipp-uas.xml` (a UAS that impersonates
the far end of an *outbound* trunk call, no real RTP), this scenario is
a SIPp **UAC** that impersonates an Indian carrier SBC placing an
**inbound** call into Tring, with a real G.711 (PCMU) SDP offer. That's
the detail that makes this test worth having: it proves FreeSWITCH's
`public` dialplan transcodes the carrier's PCMU leg down to Opus before
handing the call to the gateway's fsplug SIP UAS — the loopback smoke
test can't show that (loopback legs are raw L16, no transcode involved).

## Topology under test

```
sipp (this scenario, PCMU offer)
  -> FreeSWITCH EXTERNAL profile :5080 (vars.xml: external_sip_port=5080)
  -> dialplan/public.xml: absolute_codec_string=opus, bridge to
     sofia/gateway/tring-gw/<DID>
  -> gateway's fsplug SIP UAS :5090 (Opus only)
  -> internal/media/room.Plug -> pion room -> IVR (internal/brain + ivrdrive)
  -> internal/callcdr writes a `calls` row with source='phone'
```

## Why this test is DEFERRED here, not run

This scaffolding was authored in an environment where:
- `sipp` is **not installed**.
- The full stack (FreeSWITCH with the `public`/`tring-gw` config baked
  in, the gateway with `fsplug_enabled=true`, Postgres, a seeded
  tenant/DID/flow) is **not running**.

So `go test ./sim/inbound/` here only proves the test **compiles** and
**skips cleanly** with no env set. It does NOT prove the inbound path
actually works — that requires the manual run below, on a box with the
real stack up.

## Manual run recipe

1. **Install SIPp** (Debian/Ubuntu):
   ```bash
   sudo apt-get install -y sip-tester
   ```

2. **Bring up the stack with the plug enabled.** `fsplug_enabled`
   defaults to `false` (see `internal/config/config.go`); set
   `TRING_FSPLUG_ENABLED=true` for the gateway (env or `.env`), then:
   ```bash
   make images   # rebuilds tring-freeswitch so dialplan/public.xml +
                  # sip_profiles/external/tring-gw.xml are baked in
   make up
   ```
   Confirm the gateway logs `fsplug enabled sip=:5090` on startup.

3. **Seed a tenant + inbound DID + flow.** Through the console or API:
   create a tenant, create a flow (any greeting/menu is enough to prove
   the IVR runs), then register the inbound number:
   ```bash
   curl -X POST http://localhost:8085/v1/numbers \
     -H "Authorization: Bearer $TRING_API_KEY" \
     -H 'Content-Type: application/json' \
     -d '{"e164":"919000000001","type":"inbound","flow_id":"<flow-uuid>"}'
   ```
   (919000000001 is this test's default DID — override with
   `TRING_E2E_DID` if you use a different one.)

4. **Confirm FreeSWITCH's external profile is reachable** on
   `:5080` (`vars.xml: external_sip_port=5080`) and that
   `sip_profiles/external/tring-gw.xml` points at the gateway's
   advertised SIP address (`TRING_GATEWAY_ADVERT_IP:TRING_GATEWAY_SIP_PORT`,
   default `127.0.0.1:5090`).

5. **Run the gated test:**
   ```bash
   cd sim/inbound
   TRING_E2E=1 \
   TRING_FS_EXTERNAL=127.0.0.1:5080 \
   TRING_TEST_DSN="postgres://tring:tring_dev@localhost:5434/tring?sslmode=disable" \
   go test -run TestInboundE2E -v
   ```
   (You can also invoke SIPp directly without the Go harness, for manual
   poking: `sipp -sf invite.xml -s 919000000001 -m 1 127.0.0.1:5080`.)

## Expected observables on a correct run

- SIPp completes the call: `100`/`180` (optional) -> `200 OK` -> `ACK`
  -> (2s pause) -> `BYE` -> `200 OK`, exit code 0.
- `make logs` / gateway stdout shows something like
  `pstn: call attached ... source=phone` and the IVR greeting audio
  publishes into the room.
- A `calls` row appears with `source='phone'`, `started_at` around the
  run time — this is what `TestInboundE2E` queries and asserts on.
- Inspecting the SIP/RTP with `sngrep` or `tcpdump` on port 5080 shows
  the **carrier leg is PCMU**; a capture on the gateway's fsplug side
  (`:5090` + its RTP range, `fsplug_rtp_min`/`fsplug_rtp_max`) shows
  **Opus** — the transcode this test exists to demonstrate.

## What this test does NOT cover

- DTMF over the inbound leg (RFC 2833 -> internal DTMF bridging is
  covered by `internal/fsplug`'s own unit tests, `dtmf_test.go`).
- Outbound-trunk CDR settlement (still unwired; see
  `docs/ARCHITECTURE.md` / the outbound plan).
- Real PSTN connectivity — this is still carrier-simulator-to-FreeSWITCH,
  not a live circuit.

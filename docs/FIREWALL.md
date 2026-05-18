# Tring behind a firewall

Real PSTN trunks demand stable, predictable network reachability. Tring's components — FreeSWITCH for SIP/RTP, the Go API for REST, the browser SDK for WSS — each have specific firewall needs. This page walks through three common deployment shapes and what breaks when you skip a step.

If you only read one thing on this page: **disable SIP ALG on your router/firewall**. It is by far the most common cause of "REGISTER works but calls drop after 30 seconds" or "audio is one-way" symptoms in field deployments.

## What needs to traverse the firewall

The minimum surface for a working PSTN-connected Tring node:

| Port | Protocol | Direction | Who's on the other side | Why |
|------|----------|-----------|--------------------------|-----|
| 5060 | TCP + UDP | inbound + outbound | Telco SIP gateway | SIP REGISTER/INVITE/BYE |
| 5061 | TCP | inbound + outbound | Telco SIP gateway (if TLS) | SIP-over-TLS — needed for some trunks |
| `TRING_RTP_START_PORT`–`END_PORT` | UDP | inbound + outbound | Telco media gateway + browser agents | RTP audio + RTCP |
| 7443 | TCP | inbound | Browser agents | WSS (sip.js over WebSocket) |
| 8085 | TCP | inbound | Your CRM / integrators | Tring REST API |
| 3478 | UDP | outbound only | STUN server (if `TRING_PUBLIC_IP=stun:…`) | NAT auto-discovery |

`ESL` (8021), the internal API↔FS channel, and Postgres (5434) are local-only — they should **never** be exposed to the internet.

## Scenario 1 — Cloud VPS (AWS / GCP / DO / Hetzner)

The straightforward case. The VPS has a real public IP; cloud "firewall" is just a security group blocking inbound by default.

```bash
# .env
TRING_PUBLIC_IP=203.0.113.42        # the VPS public IP
TRING_RTP_START_PORT=16384
TRING_RTP_END_PORT=16483             # narrow the RTP range to match
```

Security-group rules:

```
inbound  tcp/5060        from 0.0.0.0/0   # or just telco's CIDR if known
inbound  udp/5060        from 0.0.0.0/0
inbound  udp/16384-16483 from 0.0.0.0/0
inbound  tcp/7443        from 0.0.0.0/0   # browser agents from anywhere
inbound  tcp/8085        from <your-crm-cidr>
inbound  tcp/22          from <your-ip>/32   # ssh, lock it down
```

No NAT translation, no port forwarding. **Allowlist your VPS IP at the telco** (Exotel/Airtel/Jio/Tata) and you're done.

## Scenario 2 — Office firewall with 1:1 NAT

The server is on a private LAN (e.g. `10.x.x.x`); the office router/firewall does NAT to a real public IP. Works but needs three things in the right order:

### 1. Port forwarding (DNAT)

On the firewall, forward the inbound public ports to the FS host's private IP:

```
public 0.0.0.0:5060/tcp     →  10.0.0.50:5060/tcp
public 0.0.0.0:5060/udp     →  10.0.0.50:5060/udp
public 0.0.0.0:16384-16483/udp  →  10.0.0.50:16384-16483/udp
public 0.0.0.0:7443/tcp     →  10.0.0.50:7443/tcp
```

If your firewall mangles port numbers (some do "port translation" not just IP translation), you must use the **same** port on both sides — SIP signaling embeds port numbers in headers and SDP, so a 5060→5061 remap will break it.

### 2. `TRING_PUBLIC_IP` = firewall's public IP

```bash
TRING_PUBLIC_IP=203.0.113.42   # NOT 10.0.0.50
```

This is the IP FS advertises in SDP `c=` lines and SIP `Contact:` / `Via:` headers. If you advertise `10.0.0.50`, the telco tries to send RTP back to `10.0.0.50` — unroutable on the internet, audio dies. One-way audio (you hear them, they hear silence) is the textbook symptom.

### 3. Disable SIP ALG

Many enterprise/SMB firewalls ship with "SIP ALG" or "SIP helper" enabled by default. It actively rewrites SIP packets in transit — IP addresses in `Via:`, port numbers in `Contact:`, sometimes whole SDP blocks. The idea is to "help" NAT traversal, but in practice it conflicts with how FS already handles NAT (via `TRING_PUBLIC_IP`), and the result is corrupted SIP.

**Symptoms of SIP ALG interference:**

- Gateway flaps REGED → NOREG every few minutes
- REGISTER works, but INVITE responses arrive at the wrong Contact
- DTMF stops working on otherwise-stable calls
- 488 Not Acceptable Here responses from telco
- Calls drop after exactly 32 seconds (the SIP transaction timeout)

Vendor-specific commands to disable it:

| Vendor | Command |
|--------|---------|
| Cisco ASA / Firepower | `no policy-map global_policy / class inspection_default / inspect sip` |
| Fortinet | `config system settings → set sip-helper disable / set sip-nat-trace disable` |
| pfSense / OPNsense | System → Advanced → Firewall & NAT → disable "Disable Auto-added VPN rules"; also `set net.inet.ip.alg.sip.enable=0` |
| Sophos UTM | Web Protection → SIP ALG: Disable |
| MikroTik | `/ip firewall service-port disable sip` |
| Most consumer routers | search the admin UI for "SIP ALG", "SIP Helper", "Application Layer Gateway" — disable all |

After disabling, restart the FS container and watch the trunk re-register cleanly.

### 4. UDP conntrack timeouts

Stateful firewalls track UDP "connections" with a conntrack table. Default UDP timeout is often 30s — long enough that a brief audio silence on a real call breaks the conntrack entry, after which RTP packets are dropped.

Fix:

- **Firewall side**: raise the UDP conntrack timeout for the RTP port range to ≥300s. On Linux: `sysctl net.netfilter.nf_conntrack_udp_timeout=300` and `…udp_timeout_stream=600`.
- **FS side**: it already sends RTCP every ~5s by default, which keeps the conntrack entry alive. Verify with `media_timeout` set in `vars.xml` — should be reasonable (30s+).

## Scenario 3 — CGNAT (typical Indian residential / many ISPs)

No real public IP. Your "public" IP is shared with hundreds of other customers via Carrier-Grade NAT, and inbound traffic can't be routed to you specifically. Your visible public IP also changes unpredictably — the telco can't allowlist a moving target.

**Tring won't work directly here.** Workarounds:

| Option | Monthly cost | Complexity | Notes |
|--------|--------------|------------|-------|
| **Move FS to a $5 VPS** | $5 (Hetzner / DigitalOcean) | low | Cleanest. The Tring stack is purpose-built for this — `docker compose up -d` on a stock VPS. |
| **WireGuard tunnel to a public-IP VPS** | $5 | medium | FS stays on-prem, VPS is a stable peering point. Tunnel must carry both 5060 + RTP range. |
| **ISP business plan with static public IP** | ₹300–1000 (India) | low | Available from most ISPs on business plans; rarely on residential. |
| **SBC service from a cloud SIP provider** | $$$ | high | Telco peers with SBC, SBC peers with your private FS over private link. Enterprise-grade, overkill for v0.1. |

For Tring v0.1, **recommended is option 1** (VPS). You can move to on-prem with a tunnel later if you need to.

## Browser agents behind NAT — TURN

So far this page has been about FreeSWITCH's reachability. The other half of the story is the **agent's browser**, which is often a residential / corporate machine that's also behind NAT — and WebRTC doesn't just work end-to-end across two NATs.

### Why STUN alone isn't enough

WebRTC uses ICE to discover candidate paths between peers (browser ↔ FreeSWITCH). Each peer offers:

- **host** candidates — the LAN IP they actually bind to
- **srflx** candidates — their public IP as seen by an external STUN server
- **relay** candidates — a public-IP TURN server willing to relay their packets

ICE pairs candidates and tries them in order. **STUN gives you srflx; TURN gives you relay.**

STUN works for the easy NAT types — full cone, restricted cone, port-restricted cone. It fails for **symmetric NAT** (some corporate firewalls, most CGNAT deployments) because the NAT assigns a different source port per destination. The srflx candidate the browser advertises won't match what FreeSWITCH actually sees, and the ICE check times out. No audio.

Symptoms when you need TURN but don't have it:

- SIP REGISTER succeeds; agent shows as registered.
- Call connects; INVITE → 200 OK → ACK fly cleanly.
- ICE gathering hangs or fails. Browser console shows `iceConnectionState: failed`.
- Audio is silent in both directions, or one-way (only the side with relax NAT reaches the other).

### How Tring wires TURN

Three env vars, returned by `/v1/sdk/credentials` as part of the `ice_servers` array, passed straight into the browser's `RTCPeerConnection`:

```bash
# .env
TRING_TURN_URL=turn:turn.example.com:3478
TRING_TURN_USERNAME=tring
TRING_TURN_PASSWORD=<long random>

# STUN default is Google's public server. Override if you don't want
# browsers reaching Google, or to point at the same coturn for both:
TRING_STUN_URL=stun:turn.example.com:3478
```

The SDK consumes this without any client-side configuration — drop the env vars in, browsers everywhere get TURN, no rebuild needed.

### Running coturn yourself

coturn is the standard open-source TURN server. Single-container deploy:

```yaml
# Add to docker-compose.yml — not bundled by default to keep the
# main stack minimal.
turn:
  image: coturn/coturn:latest
  network_mode: host
  command:
    - -n
    - --lt-cred-mech
    - --realm=turn.example.com
    - --external-ip=<your-public-ip>
    - --user=tring:<the-password-from-.env>
    - --min-port=49152
    - --max-port=49252
    - --no-cli
    - --no-tlsv1
    - --no-tlsv1_1
  restart: unless-stopped
```

Firewall: open TCP/UDP 3478 (signaling) and UDP 49152–49252 (relay range) inbound on the public IP.

For production, also configure TLS (`--cert`, `--pkey`, `turns:`-prefixed URL) — corporate firewalls increasingly block plain TURN/UDP but allow TURNS/TCP/443.

### Cloud TURN services (no infra to run)

Easier if you don't want a coturn container to operate:

| Service | Pricing model | Notes |
|---------|---------------|-------|
| **Twilio Network Traversal Service** | per-GB relayed | Battle-tested, global anycast. ~$0.40/GB. |
| **Cloudflare Calls TURN** | per-GB | Newer, cheaper; bundled with Cloudflare Calls SFU. |
| **Xirsys** | tiered | Long-running player; subscriptions from $33/mo. |
| **Metered.ca** | free tier + paid | Generous free tier for development. |

All four mint short-lived TURN credentials via an API call. If you go this route, you'll likely run a small server-side helper that mints credentials per-agent and stuffs them into the Tring env at boot (or extend `/v1/sdk/credentials` to mint per-call — Phase 2).

### Verifying TURN is actually being used

In Chrome, open `chrome://webrtc-internals` during a call. Find the active `RTCPeerConnection` and look at "Stats Tables → Selected Candidate Pair". The local candidate's `candidateType` tells you:

- `host` — direct LAN path (same network)
- `srflx` — direct via STUN (typical home network → FS)
- `relay` — via TURN (the case you care about for symmetric-NAT browsers)

If you configured TURN and never see `relay` candidates even from a known-symmetric-NAT browser, something's wrong — usually wrong credentials, firewall blocking 3478, or the TURN URL is malformed.

## Outbound-only firewalls

Some hosts have strict egress rules (DLP environments, government infra). FS needs *outbound* to:

- **Telco SIP endpoints** — the realm host on TCP/UDP 5060/5061. Find the IP with `dig <realm>` and allowlist.
- **STUN servers** — UDP 3478 to `stun.freeswitch.org` (default) or whichever you configured. Only needed if `TRING_PUBLIC_IP=stun:…`. If you set a literal IP or `host:fqdn`, you can drop STUN egress entirely.
- **PostgreSQL** (TCP 5434) — usually local container, no egress needed.
- **Container registries** — only at deploy time, for `docker pull`.

## Diagnostic checklist

When the trunk shows `NOREG` or calls have audio issues, run these in order:

```bash
# 1. Did sofia load? If not, fix configs before debugging network.
docker exec tring-fs fs_cli -x "sofia status profile external"

# 2. What IP does FS think it should advertise?
docker exec tring-fs fs_cli -x "global_getvar external_sip_ip"
# Should match your firewall's PUBLIC ip — not 10.x, not 192.168.x, not 127.0.0.1.

# 3. Can we even reach the telco's SIP port?
docker exec tring-fs sh -c "nc -zv -u <telco-realm> 5060"

# 4. Watch the REGISTER fly out and check what's in the Via header.
docker exec tring-fs fs_cli -x "sofia loglevel all 9"
docker compose logs -f freeswitch | grep -A 10 REGISTER
# The "Via:" line outgoing MUST show your firewall's public IP, not 10.x.

# 5. If REGISTER goes out but no 200 OK arrives:
#    - Wrong IP at telco allowlist
#    - SIP ALG mangling the response
#    - Firewall dropping return-path UDP

# 6. If REGISTER gets 200 OK but inbound INVITE never arrives:
#    - DNAT not set up for inbound 5060
#    - Firewall dropping unsolicited inbound (no conntrack match)

# 7. One-way audio (REGISTER ok, INVITE ok, call connects, no sound):
#    - TRING_PUBLIC_IP wrong (still advertising private IP in SDP)
#    - DNAT not set up for UDP RTP range
#    - SIP ALG rewriting SDP c= line
```

If you've checked all of the above and it still doesn't work, capture a `sngrep` trace and a `tcpdump` of UDP 5060 + the RTP range — together they tell the whole story of what's actually crossing the wire vs what FS thinks it's sending. Open an issue with redacted output.

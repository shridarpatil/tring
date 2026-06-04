# Tring behind a firewall

Tring has **two distinct public surfaces** — keep them straight:

1. **FreeSWITCH ↔ PSTN trunks** — FreeSWITCH still talks SIP/RTP to your telco
   (Exotel/Airtel/Jio/Tata). This is the bulk of this page (SIP ALG, NAT, RTP).
2. **Gateway ↔ browsers** — since the ADR-001 cutover, browser calling goes
   through the **media gateway** (`cmd/gateway`) over raw WebRTC, **not**
   FreeSWITCH-over-WebSocket. The gateway is the only browser-facing node;
   FreeSWITCH is never exposed to browsers. See `docs/EDGE_DEPLOYMENT.md` for
   the public/private split.

If you only read one thing on this page: **disable SIP ALG on your router/firewall** (on the FreeSWITCH↔telco path). It's by far the most common cause of "REGISTER works but calls drop after 30 seconds" / "audio is one-way".

## What needs to traverse the firewall

The minimum public surface for a working PSTN-connected Tring node:

| Port | Protocol | Direction | Who's on the other side | Why |
|------|----------|-----------|--------------------------|-----|
| 5060 | TCP + UDP | inbound + outbound | Telco SIP gateway | SIP REGISTER/INVITE/BYE (FreeSWITCH) |
| 5061 | TCP | inbound + outbound | Telco SIP gateway (if TLS) | SIP-over-TLS — needed for some trunks |
| `TRING_RTP_START_PORT`–`END_PORT` | UDP | inbound + outbound | Telco media gateway | RTP audio + RTCP (FreeSWITCH↔telco) |
| 8087 (→443) | TCP | inbound | Browsers | Gateway signaling: HTTPS `/webrtc/offer` + WSS `/agent/ws` — TLS-front on 443 |
| `TRING_GATEWAY_UDP_MIN`–`MAX` | UDP | inbound + outbound | Browsers | Gateway WebRTC media (DTLS-SRTP) on `TRING_PUBLIC_IP` |
| 5349 / 3478 | TCP/UDP | inbound | Browsers | TURN (coturn) — UDP-block fallback; prefer TURNS/443 |
| 8085 | TCP | inbound | Your CRM / integrators | Tring REST API (can stay **private** — see EDGE_DEPLOYMENT) |

`ESL` (8021), the gateway↔FreeSWITCH SIP/RTP leg (`5090`), Postgres (5434), Redis (6379), and FreeSWITCH itself toward browsers are **internal-only** — never exposed to the internet. **STUN is not used** (the gateway is the public endpoint — see "Browser agents behind NAT" below). FreeSWITCH's old browser WebSocket ports (`5066`/`7443`) are retired.

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
# FreeSWITCH ↔ telco (trunks)
inbound  tcp/5060        from <telco-cidr>   # SIP; tighten to telco if known
inbound  udp/5060        from <telco-cidr>
inbound  udp/16384-16483 from <telco-cidr>   # FS↔telco RTP

# Gateway ↔ browsers (web calling)
inbound  tcp/443         from 0.0.0.0/0       # gateway signaling (HTTPS+WSS), TLS-fronted → :8087
inbound  udp/10000-20000 from 0.0.0.0/0       # gateway WebRTC media (TRING_GATEWAY_UDP_*)
inbound  tcp/5349        from 0.0.0.0/0        # TURN (if co-located) — TURNS/443 ideal

inbound  tcp/8085        from <your-crm-cidr>  # REST API (omit if kept private)
inbound  tcp/22          from <your-ip>/32     # ssh, lock it down
```

No NAT translation, no port forwarding. Set `TRING_PUBLIC_IP` to the VPS IP, **allowlist your VPS IP at the telco** (Exotel/Airtel/Jio/Tata), and you're done. For scale, put the **gateway on its own VPS** (scaled horizontally) and keep FreeSWITCH private — only the gateway + TURN need public IPs then.

## Scenario 2 — Office firewall with 1:1 NAT

The server is on a private LAN (e.g. `10.x.x.x`); the office router/firewall does NAT to a real public IP. Works but needs three things in the right order:

### 1. Port forwarding (DNAT)

On the firewall, forward the inbound public ports to the FS host's private IP:

```
# FreeSWITCH ↔ telco
public 0.0.0.0:5060/tcp         →  10.0.0.50:5060/tcp
public 0.0.0.0:5060/udp         →  10.0.0.50:5060/udp
public 0.0.0.0:16384-16483/udp  →  10.0.0.50:16384-16483/udp
# Gateway ↔ browsers (if the gateway is on this host)
public 0.0.0.0:443/tcp          →  10.0.0.50:8087/tcp
public 0.0.0.0:10000-20000/udp  →  10.0.0.50:10000-20000/udp
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

The other half of the story is the **agent's / visitor's browser**, behind a residential or corporate NAT. The browser connects to the **gateway**, which is a public server — that changes the NAT story compared to peer-to-peer WebRTC.

### Why STUN isn't needed here

In the gateway model the browser isn't peering with another NATed endpoint — it's connecting to a **public server** (the gateway, advertised at `TRING_PUBLIC_IP`). The browser's outbound ICE check opens its NAT mapping, the gateway sees the browser's reflexive address on that incoming packet, and replies to exactly it. So:

- The gateway doesn't need STUN — you configure its public IP directly.
- The browser doesn't need STUN — the gateway discovers its reflexive address for it.
- **Symmetric NAT works without a relay**, because the gateway always replies to the exact source it saw (symmetric NAT only breaks *peer-to-peer*, where a third party must predict your mapping).

So `TRING_STUN_URL` is empty by default and the browser's ICE list is usually just TURN (or nothing on a LAN). What actually matters: a correct `TRING_PUBLIC_IP` and an open gateway UDP media range.

### When you still need TURN

TURN is the fallback for browsers whose network **blocks outbound UDP** entirely — locked-down corporate / guest Wi-Fi that only allows TCP/443. Without UDP, the browser can't reach the gateway's media ports at all, and the only way out is **TURN over TLS on 443** (`turns:`), which looks like ordinary HTTPS to the firewall.

Symptoms you need TURN:

- Signaling is fine (`/webrtc/offer` returns 200, or the agent WS connects), but…
- ICE never connects — browser console shows `iceConnectionState: failed`, no media.
- It only fails on certain networks (corporate/guest), works on home/mobile.

### How Tring wires TURN

Set three env vars; the API returns them in the `ice_servers` of a mint response, and the gateway uses them for its own candidates too. Passed straight into the browser's `RTCPeerConnection`:

```bash
# .env  (STUN intentionally NOT set — the gateway is the public endpoint)
TRING_TURN_URL=turns:turn.example.com:443    # TURNS/443 for UDP-blocked clients
TRING_TURN_USERNAME=tring
TRING_TURN_PASSWORD=<long random>
```

No client-side config or rebuild — drop the env vars in and browsers everywhere pick up TURN. For production, prefer **short-lived TURN REST credentials** (coturn `use-auth-secret`) minted alongside the call token rather than a static shared password.

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

All four mint short-lived TURN credentials via an API call. The clean integration is to mint them in the same place that mints the **gateway call token** (`POST /v1/guest-credentials` / the agent token endpoint) and return them in `ice_servers`, so each browser gets time-limited creds per call.

### Verifying which path a call took

In Chrome, open `chrome://webrtc-internals` during a call. Find the active `RTCPeerConnection` and look at "Stats Tables → Selected Candidate Pair". The candidate's `candidateType` tells you:

- `host` / `prflx` — the browser reached the **gateway directly** (the normal case — the gateway's public IP, no relay). `prflx` (peer-reflexive) is expected since there's no STUN.
- `relay` — via TURN (the UDP-blocked-network fallback).

If a browser on a UDP-blocking network never produces a `relay` pair, TURN is misconfigured — usually wrong creds, the firewall blocking your TURN port, or the TURN URL isn't `turns:`/443. (You do **not** need to see `srflx` — STUN is off by design.)

## Outbound-only firewalls

Some hosts have strict egress rules (DLP environments, government infra).

**FreeSWITCH** needs *outbound* to:
- **Telco SIP endpoints** — the realm host on TCP/UDP 5060/5061. Find the IP with `dig <realm>` and allowlist.
- **PostgreSQL / Redis** — usually local containers, no egress needed.

**The gateway** needs:
- *Inbound* UDP (its media range) + TCP 443 from browsers — it's a server, not an egress client.
- No STUN egress (STUN is off). If you run TURN, browsers reach it inbound; the gateway reaching TURN is only needed for relayed candidates on its own side.

**Container registries** — only at deploy time, for `docker pull`.

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

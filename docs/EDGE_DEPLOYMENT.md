# Edge deployment — keeping FreeSWITCH (and the API) off the public internet

Browser calling needs three things reachable from the visitor's browser: the
**SDK bundle**, **call credentials**, and the **signaling + media** path. None
of those require FreeSWITCH — or even the Tring API — to be directly
internet-exposed. This doc describes the public *edge* you put in front, and
two deployment postures.

See `docs/WEB_CALL_FLOW.md` for the per-call sequence; this doc is about
network topology.

## What the browser actually needs

| Need | Served by | Must be public? |
|------|-----------|-----------------|
| SDK bundle (`voice.js`) | API `/sdk/*` (or your CDN/proxy) | the bytes, yes — but via your edge, not the API |
| Call credentials | your backend (`data-credentials-url`) → Tring `POST /v1/guest-credentials` | your backend, yes; Tring API, no |
| SIP signaling | WSS reverse-proxy → FreeSWITCH `:5066` | the proxy, yes; FreeSWITCH, no |
| WebRTC media | TURN/coturn relay → FreeSWITCH RTP | TURN, yes; FreeSWITCH, no |

Everything else — the Tring API's `/v1`/`/fs`/`/admin`, FreeSWITCH's SIP/ESL,
the orchestrator, Postgres, Redis — is only ever called from inside the private
network and stays there.

## Posture A — private FreeSWITCH, public API

The simplest hardening: FreeSWITCH has no public IP; the API (and the public
guest mint) stay reachable so the browser can mint creds and fetch the bundle
directly.

```
Browser ──HTTPS──► Tring API (public)        ──► /sdk/voice.js, /v1/sdk/guest-credentials
        ──WSS────► WSS proxy (public) ──────► FreeSWITCH :5066   (private)
        ──SRTP───► TURN (public)      ──────► FreeSWITCH RTP     (private)
```

## Posture B — private FreeSWITCH AND private API

With **backend-minted credentials** (`data-credentials-url`) the browser never
calls the Tring API, so it can be private too. Only your backend, a WSS proxy,
TURN, and the static bundle are public.

```
   PUBLIC                                   PRIVATE (VPC / LAN)
   ─────────────────────────                ─────────────────────────
   Your backend  (mints creds) ───────────► Tring API   (/v1, /fs, /admin)
   WSS reverse-proxy (signaling) ─────────► FreeSWITCH   (SIP/ESL/WS)
   TURN / coturn     (media)     ─────────► Orchestrator
   voice.js          (CDN or proxied)       Postgres, Redis
```

Conditions for Posture B:
1. Browser fetches creds from **your** backend (`data-credentials-url`); your
   backend calls Tring `POST /v1/guest-credentials` server-to-server with the
   tenant API key. See `docs/WEB_CALL_FLOW.md`.
2. Serve the **bundle** without exposing the API (next section).
3. The **agent/CRM-operator widget** (`agent.js`) is the exception — it calls
   `/v1/sdk/credentials` and `/v1/sdk/calls` from the browser with its JWT. If
   agents use a browser widget, either keep a minimal public API surface (just
   `/v1/sdk/*`) or proxy those two endpoints through your backend. The
   *visitor* flow has no such dependency.

## Serving the SDK bundle

The widget is one static file (`voice.js`, also `agent.js`/`tring.js`),
produced by `make sdk`. Two ways to get it to the browser without exposing the
API:

- **Reverse-proxy `/sdk/*` through the edge** (zero extra infra). You already
  run a public proxy for WSS — add a route that forwards `/sdk/` to the private
  API. Self-host-friendly; API stays fully private.
- **CDN.** Good for the hosted SaaS and browser-edge caching. Caveat: the API
  serves `/sdk/*` with `Cache-Control: no-cache` and the filename is **not**
  content-hashed (`voice.js`, stable so the embed snippet never changes), so
  the CDN must **honor revalidation** (or use a short TTL). Caching it
  long-lived would serve a stale widget after a deploy.

## Configuration (already supported — no code change)

Point the SDK and FreeSWITCH at the edge via env (`.env`):

| Env var | Role |
|---------|------|
| `TRING_WS_URL` | The `ws_url` returned to the SDK. Set to your **WSS edge**, e.g. `wss://voice.example.com:7443`. Empty = API auto-detects the host LAN IP (dev only). |
| `TRING_TURN_URL` / `TRING_TURN_USERNAME` / `TRING_TURN_PASSWORD` | TURN relay, returned in `ice_servers`. Required when FreeSWITCH has no public IP / for symmetric-NAT browsers. |
| `TRING_STUN_URL` | STUN (default Google's). Replace with your own to avoid third-party lookups. |
| `TRING_PUBLIC_IP` | Public IP FreeSWITCH advertises in SDP for media (the edge's public IP). |
| `TRING_RTP_START_PORT` / `TRING_RTP_END_PORT` | RTP port range FreeSWITCH uses — open/forward exactly this range on the edge. |

## Sample edge configs

### Caddy (WSS proxy + optional `/sdk` passthrough)

```
voice.example.com {
    # SIP-over-WebSocket → private FreeSWITCH
    @ws header Connection *Upgrade*
    reverse_proxy @ws 10.0.0.10:5066

    # Optional: serve the widget bundle without exposing the API (Posture B)
    handle_path /sdk/* {
        reverse_proxy 10.0.0.20:8085   # private Tring API
    }
}
```

### nginx (WSS proxy)

```nginx
server {
    listen 7443 ssl;
    server_name voice.example.com;
    # ssl_certificate ...;

    location / {                       # SIP-over-WS to private FreeSWITCH
        proxy_pass http://10.0.0.10:5066;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
    }

    location /sdk/ {                   # optional bundle passthrough (Posture B)
        proxy_pass http://10.0.0.20:8085;
    }
}
```

### coturn (media relay)

```
listening-port=3478
tls-listening-port=5349
realm=voice.example.com
use-auth-secret
static-auth-secret=<shared-secret>     # or static user; rotate for prod
external-ip=<edge-public-ip>
min-port=49152
max-port=65535
```

Set `TRING_TURN_URL=turn:voice.example.com:3478` (+ credentials) so the SDK
relays media through it.

> Roadmap (`docs/TODO.md`): ephemeral TURN credentials (coturn
> `use-auth-secret`) for anonymous browsers, and a CDN/WAF in front of the
> public edge. The application already emits the `ice_servers` the SDK needs;
> wiring short-lived TURN creds is the remaining piece.

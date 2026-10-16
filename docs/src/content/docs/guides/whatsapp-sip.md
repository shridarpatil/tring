---
title: WhatsApp calls over SIP (inbound)
description: Connect a WhatsApp Business number to Tring so WhatsApp callers reach your IVR flows and agents.
---

This guide connects a WhatsApp Business phone number to a self-hosted Tring deployment, end to end. When it's done, a WhatsApp user who taps **Call** on your business chat lands in a Tring IVR flow and can reach your agents, just like a phone caller.

:::note[Scope]
Inbound (user-initiated) calls only. Outbound (business-initiated) WhatsApp calls are not supported yet.
:::

All IP addresses, hostnames and numbers below are placeholders (`203.0.113.10`, `wa-sip.example.com`, `+91XXXXXXXXXX`). Replace them with your own.

## 1. How it works

```
 WhatsApp user taps "Call"
          │
          ▼
 Meta SIP edge (wa.meta.vc)
          │  SIP over TLS (mTLS), port 5061
          │  SRTP (SDES) + Opus audio
          ▼
 ┌──────────────────────── your server ─────────────────────────┐
 │ FreeSWITCH  "whatsapp" profile  (TLS only, Meta cert only)   │
 │      │  dialplan context "whatsapp"                          │
 │      │  plain RTP, Opus                                      │
 │      ▼                                                       │
 │ tring-gateway  SIP plug (:5090)  ──► IVR flow ──► agents     │
 └──────────────────────────────────────────────────────────────┘
```

1. Meta opens a TLS connection to the hostname and port you register on the business number, and sends a SIP `INVITE` to `sip:+<your number>@<your hostname>`.
2. FreeSWITCH's `whatsapp` profile accepts the connection **only** if Meta presents its client certificate (`client.sip.fbclientcerts.com`), signed by Meta's outbound API CA.
3. The `whatsapp` dialplan context accepts encrypted audio (SDES-SRTP) only, and bridges the call to the Tring gateway, the same path a carrier (PSTN) call takes.
4. The gateway looks up the dialed number, runs its IVR flow, and routes to agents. Call records, recordings and webhooks work the same as for phone calls.

The `whatsapp` profile is independent of your carrier trunk profile. Both run at the same time on different IPs and ports.

## 2. Before you start

### Meta side

- A **WhatsApp Business Account** with the phone number registered on the **Cloud API**.
- A Meta app that has **messaging** working for that number (send and receive a test message first), in **Live** mode, not Development.
- The calling prerequisites from Meta's [Calling API docs](https://developers.facebook.com/docs/whatsapp/cloud-api/calling) are met for the number.
- A **system user access token** with `whatsapp_business_management` (and `whatsapp_business_messaging`) for that app.
- The **phone number ID** of the business number.

:::caution
Once SIP is enabled on a number, Meta's Graph API calling endpoints stop working for it. A number can have only **one** SIP server.
:::

### Tring side

- Tring is deployed with Docker Compose (`tring-fs`, `tring-gateway`, `tring-api` running).
- The SIP plug is on: `TRING_FSPLUG_ENABLED=true`, with `TRING_GATEWAY_ADVERT_IP` / `TRING_GATEWAY_SIP_PORT` matching `TRING_FSPLUG_SIP_ADVERTISE` / `TRING_FSPLUG_SIP_LISTEN`. If inbound phone calls from a carrier already work, this is done.
- An IVR flow exists for WhatsApp callers (it can be the same flow your phone number uses).
- A Tring API key for your tenant (for the API steps), or console access.

### Infrastructure

- A **public IPv4 address** that reaches the server (on AWS, an Elastic IP on the instance's primary interface).
- A **DNS name** you control, e.g. `wa-sip.example.com`.
- The ability to edit the **firewall / security group**.

## 3. Network and firewall

### 3.1 Pick the IPs

| Setting | What it is | Example |
|---|---|---|
| `TRING_WHATSAPP_IP` | Private IP FreeSWITCH binds for WhatsApp (the interface that holds the public IP) | `10.0.1.20` |
| `TRING_WHATSAPP_PUBLIC_IP` | Public IP Meta connects to; advertised in SIP and SDP | `203.0.113.10` |

If you also have a carrier trunk on a separate private interface (e.g. a SIP-over-AWS circuit on a second ENI), keep it there. WhatsApp uses the **public** interface.

### 3.2 Open only what Meta needs

| Direction | Protocol / port | Source | Purpose |
|---|---|---|---|
| Inbound | TCP **5061** (or your `TRING_WHATSAPP_TLS_PORT`) | Meta IP ranges only | SIP over TLS |
| Inbound | UDP **RTP range** (`TRING_RTP_START_PORT`–`TRING_RTP_END_PORT`, default 16384–32768) | Meta IP ranges only | Audio |
| Outbound | Same ports | Meta IP ranges | Replies and audio (default "allow all egress" covers this) |

:::danger
Do not open these to `0.0.0.0/0`.
:::

Meta's SIP and media IPs are the same as its webhook IPs ([Cloud API webhook IP addresses](https://developers.facebook.com/docs/whatsapp/cloud-api/guides/set-up-webhooks)). Meta publishes them through its AS number:

```bash
whois -h whois.radb.net -- '-i origin AS32934' | awk '/^route:/ {print $2}' | sort -u > meta-v4.txt
```

That list is long. Collapse it before turning it into rules:

```bash
python3 -c 'import ipaddress,sys; [print(n) for n in ipaddress.collapse_addresses(ipaddress.ip_network(l.strip()) for l in open(sys.argv[1]) if l.strip())]' meta-v4.txt > meta-v4-collapsed.txt
wc -l meta-v4-collapsed.txt
```

On AWS, put the collapsed list in a **customer-managed prefix list** and reference it from the security group. A prefix list counts against the security group's rule quota by its *max entries*, so size it accordingly. Refresh the list periodically, because Meta's ranges change.

Narrow the RTP range if you can (e.g. `TRING_RTP_START_PORT=16384`, `TRING_RTP_END_PORT=16584`), so fewer ports are open.

Publish only an **A record** (IPv4) for the hostname unless your server is reachable over IPv6 too.

## 4. DNS

Create an A record:

```
wa-sip.example.com.   300   IN   A   203.0.113.10
```

Check it:

```bash
dig +short wa-sip.example.com
```

## 5. TLS certificates

FreeSWITCH needs two files in `freeswitch/tls/whatsapp/` inside your Tring checkout (the directory is gitignored and mounted read-only into the container):

| File | Contents |
|---|---|
| `agent.pem` | Your server certificate **chain** followed by its **private key**, for `wa-sip.example.com`, from a public CA |
| `cafile.pem` | **Meta's outbound API CA** certificate, used to verify Meta's client certificate |

### 5.1 Server certificate (Let's Encrypt)

Use the **DNS-01** challenge so you don't have to open port 80. Example with certbot (use the DNS plugin for your provider, or `--manual`):

```bash
sudo certbot certonly --manual --preferred-challenges dns -d wa-sip.example.com
```

Then build `agent.pem`:

```bash
cd /path/to/tring
mkdir -p freeswitch/tls/whatsapp
sudo sh -c 'umask 077; cat /etc/letsencrypt/live/wa-sip.example.com/fullchain.pem \
                          /etc/letsencrypt/live/wa-sip.example.com/privkey.pem \
                          > freeswitch/tls/whatsapp/agent.pem'
```

:::caution
`agent.pem` contains the private key. Keep it readable by root only, never commit it, and don't copy it anywhere else.
:::

### 5.2 Meta's CA certificate

Download the **Meta outbound API CA certificate** from the *Security* section of Meta's [SIP configuration guide](https://developers.facebook.com/docs/whatsapp/cloud-api/calling/sip). Save it as `freeswitch/tls/whatsapp/cafile.pem`, in PEM format. If the download is DER (binary), convert it:

```bash
openssl x509 -inform DER -in meta-ca.crt -out freeswitch/tls/whatsapp/cafile.pem
```

Check both files:

```bash
openssl x509 -in freeswitch/tls/whatsapp/cafile.pem -noout -subject -enddate
openssl x509 -in freeswitch/tls/whatsapp/agent.pem  -noout -subject -ext subjectAltName -enddate
```

The second command must list `DNS:wa-sip.example.com`.

### 5.3 Renewal

Let's Encrypt certificates last 90 days. FreeSWITCH reads the certificate when the profile starts, so after each renewal rebuild `agent.pem` and restart the profile. A certbot deploy hook does this automatically. Save it as `/etc/letsencrypt/renewal-hooks/deploy/tring-whatsapp.sh` and make it executable (`chmod 700`):

```sh
#!/bin/sh
set -eu
LIVE=/etc/letsencrypt/live/wa-sip.example.com
DEST=/path/to/tring/freeswitch/tls/whatsapp
umask 077
cat "$LIVE/fullchain.pem" "$LIVE/privkey.pem" > "$DEST/agent.pem.new"
mv "$DEST/agent.pem.new" "$DEST/agent.pem"
docker exec tring-fs fs_cli -x "sofia profile whatsapp restart"
```

:::caution
A profile restart **drops WhatsApp calls in progress** and takes about 30 seconds to come back. Phone calls on the carrier profile are not affected. If that matters, run renewals at a quiet hour instead of relying on certbot's random schedule.
:::

If you used `--manual` DNS validation, renewals also need manual DNS steps. Use your DNS provider's certbot plugin for unattended renewal.

## 6. Configure Tring

### 6.1 Environment

Add to `.env` (see `.env.example` for the full reference):

```bash
TRING_WHATSAPP_ENABLED=true
TRING_WHATSAPP_IP=10.0.1.20          # private IP of the public-facing interface
TRING_WHATSAPP_PUBLIC_IP=203.0.113.10 # Elastic / public IP; defaults to TRING_PUBLIC_IP
TRING_WHATSAPP_TLS_PORT=5061          # must match the port you register with Meta
```

| Variable | Default | Notes |
|---|---|---|
| `TRING_WHATSAPP_ENABLED` | `false` | When not `true`, the profile isn't loaded and nothing listens. |
| `TRING_WHATSAPP_IP` | primary interface IP | IP the profile binds for SIP and RTP. |
| `TRING_WHATSAPP_PUBLIC_IP` | `TRING_PUBLIC_IP`, else the bind IP | Advertised to Meta in `Contact` and SDP. Must be the IP Meta can reach. |
| `TRING_WHATSAPP_TLS_PORT` | `5061` | TLS port. No UDP or TCP listener is opened. |

### 6.2 Recreate FreeSWITCH

```bash
docker compose pull freeswitch      # or: docker compose build freeswitch
docker compose up -d freeswitch
```

### 6.3 Check the profile

```bash
docker exec tring-fs fs_cli -x "sofia status" | grep whatsapp
```

Expected:

```
whatsapp   profile   sip:mod_sofia@203.0.113.10:5061   RUNNING (0) (TLS)
```

Then check the certificate the way Meta will see it. Run this **on the server** (your own IP isn't in the security group):

```bash
openssl s_client -quiet -connect 10.0.1.20:5061 -servername wa-sip.example.com \
  -verify_hostname wa-sip.example.com </dev/null
```

Expected: `verify return:1` lines for each certificate in the chain, then an alert such as `tlsv13 alert certificate required`. That alert is correct: the profile demands Meta's client certificate, and `openssl` didn't send one. A `hostname mismatch` or `unable to get local issuer certificate` error means `agent.pem` is wrong (missing chain, or wrong name).

### 6.4 Add the WhatsApp number

Meta dials `sip:+<E.164>@<host>`, and Tring's inbound lookup is an **exact match**, so store the number **with the leading `+`**.

In the console: **Numbers → Add**, type **Inbound**, number `+91XXXXXXXXXX`, and choose the flow.

Or with the API:

```bash
curl -sS -X POST "$TRING_URL/v1/numbers" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"e164":"+91XXXXXXXXXX","type":"inbound","flow_id":"<flow-uuid>"}'
```

Keep `TRING_API_KEY` in an environment variable or secret manager, not in scripts or shell history.

## 7. Configure Meta

Keep the Meta token in an environment variable:

```bash
export WA_TOKEN='<system user access token>'   # never commit or paste into chats/tickets
export PHONE_NUMBER_ID='<phone number id>'
export GRAPH_VERSION='v23.0'                    # use the current Graph API version
```

### 7.1 Enable calling and SIP

```bash
curl -sS -X POST "https://graph.facebook.com/$GRAPH_VERSION/$PHONE_NUMBER_ID/settings" \
  -H "Authorization: Bearer $WA_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "calling": {
      "status": "ENABLED",
      "call_icon_visibility": "DEFAULT",
      "srtp_key_exchange_protocol": "SDES",
      "sip": {
        "status": "ENABLED",
        "servers": [ { "hostname": "wa-sip.example.com", "port": 5061 } ]
      }
    }
  }'
```

Expected: `{"success":true}`.

- `srtp_key_exchange_protocol: "SDES"` is **required**. Tring's profile accepts SDES-SRTP only; with Meta's default (DTLS) every call fails with `488 Not Acceptable Here`.
- `call_icon_visibility: "DEFAULT"` shows the call button in the business chat.
- Optional: add `"webhook_delivery": "ENABLED"` inside `sip` to also get `call_created` / `terminate` webhooks on your WhatsApp webhook endpoint.

### 7.2 Verify

```bash
curl -sS "https://graph.facebook.com/$GRAPH_VERSION/$PHONE_NUMBER_ID/settings" \
  -H "Authorization: Bearer $WA_TOKEN"
```

Check that `calling.status`, `calling.sip.status` are `ENABLED`, `srtp_key_exchange_protocol` is `SDES`, and `servers` has exactly one entry with your hostname. If you see servers from other apps, clear them with those apps' tokens: Meta ends the call when *any* configured server sends `BYE`.

You don't need the Meta-generated SIP password (`include_sip_credentials=true`) for inbound calls: Tring authenticates Meta with mutual TLS instead of digest auth.

## 8. Test a call

1. Turn on SIP tracing for the profile:

   ```bash
   docker exec tring-fs fs_cli -x "sofia profile whatsapp siptrace on"
   ```

2. Follow the logs in two terminals:

   ```bash
   docker logs -f tring-fs 2>&1 | grep -iE 'whatsapp|INVITE|SIP/2.0'
   docker logs -f tring-gateway
   ```

3. From a **personal** WhatsApp account (not the business number), open the business chat and tap **Call**.

4. You should see, in order:
   - an `INVITE sip:+91XXXXXXXXXX@wa-sip.example.com;transport=tls` arriving on the `whatsapp` profile;
   - the dialplan `whatsapp_inbound_to_gateway` bridging to `tring-gw`;
   - the gateway answering and starting the flow;
   - FreeSWITCH replying `200 OK` with `m=audio … RTP/SAVP`, an `a=crypto:` line and `c=IN IP4 203.0.113.10`.

5. Check the IVR prompt plays, DTMF works, an agent can take the call, and hanging up on either side ends it on both sides.

6. Turn tracing off again: `sofia profile whatsapp siptrace off`.

The call appears in Tring's call history with the WhatsApp number as the dialed number.

## 9. Caller identity

The caller's identity comes from the SIP `From` header and is available in flows as `{{caller}}` (and the dialed business number as `{{did}}`).

Meta may identify a user by a **business-scoped user ID (BSUID)** instead of a phone number, for example when the user has a WhatsApp username. In that case `{{caller}}` holds that ID, not a number. Don't assume it's dialable, and don't pass it to a "transfer to number" node.

Meta's extra headers (`x-wa-meta-user-id`, `x-wa-meta-username`, `x-wa-meta-wacid`, call-button payloads) are not passed into flow variables yet.

## 10. Troubleshooting

| Symptom | Likely cause | What to check |
|---|---|---|
| Nothing arrives on the server | Firewall, DNS, or Meta config | `dig` the hostname; security group allows Meta ranges on 5061/tcp; `GET …/settings` shows your hostname and port; `sofia status` shows the profile `RUNNING`. |
| TCP connects but no `INVITE` is logged | TLS handshake failing | Server side: run the `openssl s_client` check from section 6.3 (chain, hostname, expiry). Client side: `cafile.pem` must be Meta's outbound API CA. A wrong CA makes every Meta connection fail. Look for TLS errors in `docker logs tring-fs`. |
| Profile missing from `sofia status` | Not enabled or bad certs | `TRING_WHATSAPP_ENABLED=true` in `.env`, container recreated; both PEM files exist and are mounted at `/etc/freeswitch/tls/whatsapp/`; errors in `docker logs tring-fs`. After a restart, wait about 30 seconds. |
| `488 Not Acceptable Here` | Meta is offering DTLS or plain RTP | Set `srtp_key_exchange_protocol` to `SDES` (section 7.1). |
| `404 Not Found` | The request URI isn't a `+digits` number | Check the `INVITE` line in the trace. Meta must be dialing the phone number (no `request_uri_user_params` such as `;tgrp=` unless you also change the dialplan). |
| Call reaches the gateway but is rejected / no flow | Number format | The number in Tring must match the `INVITE` user exactly, including the `+`. |
| `503 Service Unavailable` | Gateway unreachable | `TRING_FSPLUG_ENABLED=true`; `tring-gateway` running; `TRING_GATEWAY_ADVERT_IP:TRING_GATEWAY_SIP_PORT` matches the plug's listen address. Phone calls from your carrier would fail the same way. |
| Call connects, no audio either way | RTP blocked or wrong advertised IP | The `200 OK` SDP `c=` line must be `TRING_WHATSAPP_PUBLIC_IP`; UDP RTP range open to Meta; on AWS the Elastic IP is on the interface you bind. |
| Call never ends when the WhatsApp user hangs up | In-dialog requests not reaching you | Meta sends `BYE` to the `Contact` (`…@<public IP>:5061;transport=tls`). Check the security group, and that `TRING_WHATSAPP_PUBLIC_IP` is correct. |
| Works, then fails after ~90 days | Certificate expired | Check the renewal hook (section 5.3); `openssl x509 -enddate`. |

Useful commands:

```bash
docker exec tring-fs fs_cli -x "sofia status profile whatsapp"
docker exec tring-fs fs_cli -x "show channels"
docker exec tring-fs fs_cli -x "sofia profile whatsapp restart"
```

## 11. Security notes

- **Mutual TLS** is the authentication. The profile accepts only clients with a certificate whose subject is `client.sip.fbclientcerts.com`, signed by the CA in `cafile.pem`. Other clients, including ones with valid certificates from other CAs or for other names, are refused during the handshake.
- **The firewall is the second layer.** Allow only Meta's ranges, and apply rules to the packet source address, not to addresses in SIP headers.
- The profile is **TLS 1.2+ only**, opens **no UDP or TCP SIP listener**, accepts **no registrations**, has no outbound gateways, and uses its own dialplan context that can only reach the Tring gateway.
- Audio between Meta and FreeSWITCH is **SRTP**. The FreeSWITCH-to-gateway hop is plain RTP on the host or private network, like carrier calls.
- **Secrets:** `agent.pem` (private key), the Meta access token and the Tring API key must stay out of git, tickets and chat. Use a secret manager where you can, and rotate the Meta token and Tring API key if they're ever exposed.
- Turn on **MFA** for the Meta Business account and the cloud account that hosts Tring, and keep cloud audit logging (e.g. CloudTrail) on for changes to the security group.

## 12. Limitations

- **Inbound only.** Business-initiated WhatsApp calls (which need user call permission and digest auth towards Meta) aren't supported yet.
- **One SIP server per number** on Meta's side.
- **Graph API calling endpoints are disabled** for a number while SIP is enabled.
- Meta's `x-wa-meta-*` headers aren't exposed to flows yet.
- Renewing the certificate restarts the profile and drops WhatsApp calls in progress.

## Reference: files

| Path (in the repo) | Purpose |
|---|---|
| `freeswitch/conf/sip_profiles/whatsapp/profile.xml` | The TLS-only `whatsapp` SIP profile |
| `freeswitch/conf/sip_profiles/whatsapp.xml` | Loads the profile only when `TRING_WHATSAPP_ENABLED=true` |
| `freeswitch/conf/dialplan/whatsapp.xml` | `whatsapp` context: SRTP required, bridge to the gateway, 404 otherwise |
| `freeswitch/conf/vars.xml` | Reads the `TRING_WHATSAPP_*` variables |
| `freeswitch/tls/whatsapp/` (host, gitignored) | `agent.pem`, `cafile.pem`, mounted read-only into `tring-fs` |

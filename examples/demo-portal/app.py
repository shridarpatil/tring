"""
Combined Tring demo backend — one Flask app that hosts ALL THREE web-calling
flows so you can test them without running separate servers:

  /            landing page with links to the three demos
  /support     ANONYMOUS "call us"     — voice.js, data-gateway-url + data-flow-id
               (no login, no token; the call is flow-pinned and can't reach a trunk)
  /portal      LOGGED-IN support       — voice.js, data-credentials-url → this backend
               mints a TRUSTED call token carrying the verified client_ref
               (the browser never holds the API key, so identity can't be spoofed)
  /crm         AGENT (CRM)             — agent.js; this backend mints a short-lived
               agent JWT (data-token-url) so the agent's browser can place/receive calls

How the trusted flows reach Tring (the API key stays server-side):

    browser ──/guest-credentials or /agent/token──►  THIS Flask backend
                                                      │  (holds TRING_API_KEY)
                                                      ▼
              Tring API  POST /v1/guest-credentials  (gateway call token + client_ref)
                         GET  /v1/agents/<id>/token  (agent JWT)
                                                      │
              ◄──────────────── token ───────────────┘
    browser uses the token with the Tring widget → places the call over WebRTC.

Run:
    pip install -r requirements.txt
    export TRING_API_BASE=http://localhost:8085     # Tring REST API
    export TRING_GATEWAY_URL=http://localhost:8087  # media gateway (browser-reachable)
    export TRING_API_KEY=tk_live_...                # your tenant's API key
    export TRING_FLOW_ID=<flow-uuid>                # the IVR flow visitors land in
    export TRING_AGENT_ID=<agent-uuid>              # optional; else the first agent
    python app.py
    # open http://localhost:5000  (log in as alice / demo for the portal)
"""

import json
import os
import secrets
import urllib.error
import urllib.request

from flask import Flask, abort, jsonify, redirect, render_template_string, request, session

# ── Tring connection (server-side only — the API key never reaches the browser)
API_BASE = os.environ.get("TRING_API_BASE", "http://localhost:8085").rstrip("/")
GATEWAY_URL = os.environ.get("TRING_GATEWAY_URL", "http://localhost:8087").rstrip("/")
API_KEY = os.environ.get("TRING_API_KEY", "")
FLOW_ID = os.environ.get("TRING_FLOW_ID", "")    # IVR flow visitors land in
AGENT_ID = os.environ.get("TRING_AGENT_ID", "")  # optional; else the first agent

app = Flask(__name__)
app.secret_key = os.environ.get("FLASK_SECRET", secrets.token_hex(16))

# Stand-in for your real auth / customer records. Each "user" maps to the
# client identity you want attributed on the call log.
DEMO_USERS = {
    "alice": {"password": "demo", "client_ref": "cust_ACME_42", "name": "Alice — ACME Corp"},
    "bob": {"password": "demo", "client_ref": "cust_GLOBEX_7", "name": "Bob — Globex"},
}

# Stand-in for your CRM's customer records. The agent UI shows the NAME + ref;
# the phone number lives only here on the backend — it's never sent to the
# agent's browser. The agent clicks a row → we resolve id → number and originate.
DEMO_CUSTOMERS = {
    "c1": {"name": "Aanya Sharma", "number": "+919876543210", "label": "Aanya · #4821"},
    "c2": {"name": "Rahul Patel", "number": "+918888001122", "label": "Rahul · #4822"},
    "c3": {"name": "Echo test (FS extension)", "number": "9196", "label": "Echo test"},
}


# ── Tring API helpers (the API key authorizes these; it stays on the server) ──

def _api(method: str, path: str, body: dict | None = None):
    """Call the Tring REST API with the tenant API key. Returns (status, json)."""
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Authorization": f"Bearer {API_KEY}"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(f"{API_BASE}{path}", data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.load(e)
        except Exception:
            return e.code, {"error": f"request failed ({e.code})"}
    except urllib.error.URLError as e:
        return 502, {"error": f"cannot reach Tring at {API_BASE}: {e.reason}"}


def mint_credentials(client_ref: str = ""):
    """Backend guest mint via the API key. With client_ref (logged-in) Tring
    trusts it and stamps it on the CDR; without it (anonymous guest) you get a
    plain flow-pinned token. Same endpoint for both — that's the point."""
    body = {}
    if client_ref:
        body["client_ref"] = client_ref
    if FLOW_ID:
        body["flow_id"] = FLOW_ID
    return _api("POST", "/v1/guest-credentials", body)


def resolve_agent_id():
    """The agent to mint a token for: TRING_AGENT_ID, else the tenant's first."""
    if AGENT_ID:
        return AGENT_ID, None
    status, payload = _api("GET", "/v1/agents")
    if status != 200 or not isinstance(payload, list) or not payload:
        return None, "no agents found — create one in the console or set TRING_AGENT_ID"
    return payload[0]["id"], None


# ── Routes ────────────────────────────────────────────────────────────────────

@app.get("/")
def landing():
    return render_template_string(
        LANDING_HTML,
        api_base=API_BASE,
        gateway_url=GATEWAY_URL,
        flow_id=FLOW_ID,
        has_key=bool(API_KEY),
        user=session.get("user"),
    )


# --- 1. Anonymous "call us" (no login, no token) --------------------------------
@app.get("/support")
def support():
    return render_template_string(SUPPORT_HTML, api_base=API_BASE)


@app.get("/support-credentials")
def support_credentials():
    """voice.js data-credentials-url target for the ANONYMOUS guest. No session
    and (in this demo) no CAPTCHA — in production you'd gate this with a CAPTCHA
    / rate-limit before minting, since there's no login. Mints a guest token
    (no client_ref) via the backend, so the gateway URL + flow are server-side
    and the gateway's require_auth is satisfied without a public mint."""
    if not API_KEY:
        return jsonify({"error": "server misconfigured: TRING_API_KEY not set"}), 500
    status, payload = mint_credentials()  # no client_ref → anonymous guest token
    return jsonify(payload), status


# --- 2. Logged-in trusted support (client_ref minted server-side) ---------------
@app.post("/login")
def login():
    username = request.form.get("username", "")
    password = request.form.get("password", "")
    u = DEMO_USERS.get(username)
    if not u or u["password"] != password:
        return render_template_string(LOGIN_HTML, error="Invalid login (try alice / demo)."), 401
    session["user"] = username
    return redirect("/portal")


@app.post("/logout")
def logout():
    session.clear()
    return redirect("/")


@app.get("/portal")
def portal():
    user = session.get("user")
    if not user:
        return render_template_string(LOGIN_HTML)
    u = DEMO_USERS[user]
    return render_template_string(
        PORTAL_HTML, name=u["name"], client_ref=u["client_ref"], api_base=API_BASE
    )


@app.get("/guest-credentials")
def guest_credentials():
    """voice.js data-credentials-url target. Same-origin + cookie-authed, so only
    a logged-in visitor can mint — and only ever as THEIR client_ref."""
    user = session.get("user")
    if not user:
        abort(401)
    if not API_KEY:
        return jsonify({"error": "server misconfigured: TRING_API_KEY not set"}), 500
    status, payload = mint_credentials(DEMO_USERS[user]["client_ref"])
    return jsonify(payload), status


# --- 3. Agent CRM (agent.js + a short-lived agent JWT) --------------------------
@app.get("/crm")
def crm():
    aid, err = resolve_agent_id() if API_KEY else (None, "TRING_API_KEY not set")
    # Only id + name reach the page — never the number.
    customers = [{"id": cid, "name": c["name"]} for cid, c in DEMO_CUSTOMERS.items()]
    return render_template_string(
        CRM_HTML, api_base=API_BASE, gateway_url=GATEWAY_URL,
        agent_id=aid, agent_err=err, customers=customers,
    )


@app.get("/agent/token")
def agent_token():
    """agent.js data-token-url target: mint a fresh 5-min agent JWT server-side."""
    if not API_KEY:
        return jsonify({"error": "server misconfigured: TRING_API_KEY not set"}), 500
    aid, err = resolve_agent_id()
    if err:
        return jsonify({"error": err}), 502
    status, payload = _api("GET", f"/v1/agents/{aid}/token")
    # The Tring API returns {token, gateway_url, ice_servers}; pin gateway_url to
    # this demo's browser-reachable value so the agent.js tag needs only
    # data-token-url (no data-gateway-url).
    if status == 200 and isinstance(payload, dict):
        payload["gateway_url"] = GATEWAY_URL
    return jsonify(payload), status


@app.post("/agent/originate")
def agent_originate():
    """The CRM's own 'Call' button hits THIS endpoint with a customer id (never a
    number). We resolve the number server-side and ask Tring to originate via
    POST /v1/calls — so the agent's browser never sees the dialed number. We pass
    agent_label (a non-PII string) as what the agent sees for the call."""
    if not API_KEY:
        return jsonify({"error": "server misconfigured: TRING_API_KEY not set"}), 500
    aid, err = resolve_agent_id()
    if err:
        return jsonify({"error": err}), 502
    cust = DEMO_CUSTOMERS.get((request.json or {}).get("customer", ""))
    if not cust:
        return jsonify({"error": "unknown customer"}), 400
    # API key authorizes it; the browser sent only the customer id, not the number.
    status, payload = _api("POST", "/v1/calls", {
        "agent_id": aid,
        "to": cust["number"],
        "agent_label": cust["label"],
    })
    return jsonify(payload), status


# ── Templates ──────────────────────────────────────────────────────────────────

_STYLE = """
  body { font: 15px/1.6 system-ui, sans-serif; max-width: 680px; margin: 3rem auto; padding: 0 1rem; color: #111827; }
  h1 { margin: 0 0 .25rem; }
  .hint { color: #6b7280; font-size: 13px; margin: 0 0 1.5rem; }
  .card { border: 1px solid #e5e7eb; border-radius: 12px; padding: 1.5rem; margin-top: 1rem; }
  .you { background: #ecfdf5; border: 1px solid #a7f3d0; border-radius: 8px; padding: .75rem 1rem; font-size: 13px; }
  .warn { background: #fef3c7; border: 1px solid #fbbf24; border-radius: 8px; padding: .75rem 1rem; font-size: 13px; }
  code { background: rgba(0,0,0,.06); padding: .1rem .3rem; border-radius: 3px; }
  a.demo { display: block; padding: .9rem 1rem; border: 1px solid #e5e7eb; border-radius: 8px; margin-top: .6rem; text-decoration: none; color: inherit; }
  a.demo:hover { background: #f9fafb; }
  a.demo b { color: #2563eb; }
  table { width: 100%; border-collapse: collapse; margin-top: 1rem; }
  th, td { text-align: left; padding: .6rem .5rem; border-bottom: 1px solid #e5e7eb; }
  th { font-size: 12px; text-transform: uppercase; color: #6b7280; }
  input { font: inherit; padding: .45rem .5rem; width: 100%; border: 1px solid #d1d5db; border-radius: 6px; box-sizing: border-box; margin-bottom: .5rem; }
  button { font: inherit; padding: .5rem 1rem; background: #2563eb; color: #fff; border: 0; border-radius: 6px; cursor: pointer; }
  button.link { background: none; color: #6b7280; padding: .25rem 0; text-decoration: underline; }
  .topnav { font-size: 13px; margin-bottom: 1rem; }
  .topnav a { color: #6b7280; margin-right: 1rem; }
"""

_NAV = '<div class="topnav"><a href="/">← all demos</a></div>'

LANDING_HTML = (
    """<!doctype html><meta charset="utf-8"><title>Tring — web calling demos</title>
<style>""" + _STYLE + """</style>
<body>
  <h1>📞 Tring web-calling demos</h1>
  <p class="hint">One backend, three flows. Pick one to test.</p>

  {% if not has_key %}<p class="warn">⚠ <code>TRING_API_KEY</code> is not set —
    the logged-in support and agent CRM demos need it to mint tokens.</p>{% endif %}

  <a class="demo" href="/support"><b>1 · Anonymous “call us”</b><br>
    <span class="hint">Visitor with no account taps Call → lands in your IVR. No login; a guest token is minted server-side (gate with CAPTCHA in prod).</span></a>
  <a class="demo" href="/portal"><b>2 · Logged-in support (trusted identity)</b><br>
    <span class="hint">Backend mints a call token with the verified <code>client_ref</code> — shows on the CDR, can't be spoofed.</span></a>
  <a class="demo" href="/crm"><b>3 · Agent CRM</b><br>
    <span class="hint">Agent receives/places calls. Backend mints a short-lived agent JWT.</span></a>

  <div class="card" style="margin-top:1.5rem">
    <p class="hint" style="margin:0">Config &nbsp;·&nbsp; API <code>{{ api_base }}</code>
      &nbsp;·&nbsp; gateway <code>{{ gateway_url }}</code>
      &nbsp;·&nbsp; flow <code>{{ flow_id or '(tenant default)' }}</code>
      &nbsp;·&nbsp; API key <code>{{ 'set' if has_key else 'MISSING' }}</code></p>
  </div>
</body>"""
)

SUPPORT_HTML = (
    """<!doctype html><meta charset="utf-8"><title>Acme — Support (anonymous)</title>
<style>""" + _STYLE + """</style>
<body>
  """ + _NAV + """
  <h1>🛟 Acme Help Center</h1>
  <p class="hint">Anonymous visitor page — no account. Tap <strong>Talk to a
  human</strong> and you land in the IVR over WebRTC. The widget fetches a guest
  token from this backend's <code>/support-credentials</code> (which mints via
  the API key — no <code>client_ref</code>), so the gateway URL + flow live
  server-side and the gateway's <code>require_auth</code> is satisfied without
  any public mint. The call is flow-pinned so it can never reach a trunk.
  <em>In production you'd gate <code>/support-credentials</code> with a CAPTCHA.</em></p>

  <p class="hint">This demo uses an <strong>inline button</strong> (your own
  markup) rather than the widget's floating launcher — see the
  <code>data-render-button="false"</code> + <code>&lt;button data-tring-call&gt;</code>
  below. (The logged-in <a href="/portal">/portal</a> demo shows the floating
  launcher instead.)</p>

  <div class="card">
    <h3 style="margin-top:0">Talk to us</h3>
    <p>One tap connects you to support — no number to dial.</p>
    <!-- Inline button: with data-render-button="false" on the script, the widget
         binds THIS button instead of drawing its own floating launcher. Put
         data-tring-call on any element to place the trigger wherever you want. -->
    <button data-tring-call>📞 Talk to a human</button>
  </div>

  <!-- Backend-minted guest token: the widget GETs /support-credentials for
       { gateway_url, flow_id, token } — no data-gateway-url / data-flow-id, the
       flow id stays in the token. Same mechanism as the logged-in /portal flow,
       just without a client_ref. -->
  <script src="{{ api_base }}/sdk/voice.js"
          data-credentials-url="/support-credentials"
          data-render-button="false"
          data-title="Acme Support"></script>
</body>"""
)

LOGIN_HTML = (
    """<!doctype html><meta charset="utf-8"><title>Acme — Support sign in</title>
<style>""" + _STYLE + """</style>
<body>
  """ + _NAV + """
  <h1>🛟 Acme Help Center</h1>
  <p class="hint">Sign in to talk to support. Once logged in, your call is placed
  with <strong>your verified identity</strong> — the call log shows who you are,
  not an anonymous guest.</p>
  <div class="card">
    {% if error %}<p style="color:#c00">{{ error }}</p>{% endif %}
    <form method="post" action="/login">
      <input name="username" type="text" placeholder="username — alice or bob" autocomplete="username">
      <input name="password" type="password" placeholder="password — demo" autocomplete="current-password">
      <button>Sign in</button>
    </form>
  </div>
</body>"""
)

PORTAL_HTML = (
    """<!doctype html><meta charset="utf-8"><title>Acme — Support</title>
<style>""" + _STYLE + """</style>
<body>
  """ + _NAV + """
  <h1>🛟 Acme Help Center</h1>
  <p class="hint">Signed in. Tap <strong>Call support</strong> any time.</p>

  <p class="you">Signed in as <strong>{{ name }}</strong> ·
    verified id <code>{{ client_ref }}</code></p>

  <div class="card">
    <h3 style="margin-top:0">Still need help?</h3>
    <p>One tap connects you to an agent over the internet — no number to dial.</p>
    <p style="font-size:13px;color:#6b7280">
      Because you're logged in, this page's <em>backend</em> mints the call token
      with your verified id <code>{{ client_ref }}</code> — the browser never sets
      it, so it can't be spoofed. The call shows in Tring's log as
      <code>{{ client_ref }}</code>, not <code>guest_…</code>.
    </p>
    <form method="post" action="/logout"><button class="link">Log out</button></form>
  </div>

  <!-- Trusted path: connection details + a client_ref-bearing call token come
       from THIS app (data-credentials-url), minted with the tenant API key. -->
  <script src="{{ api_base }}/sdk/voice.js"
          data-credentials-url="/guest-credentials"
          data-button-label="Call support"
          data-title="Acme Support"></script>
</body>"""
)

CRM_HTML = (
    """<!doctype html><meta charset="utf-8"><title>Acme Sales — CRM</title>
<style>""" + _STYLE + """</style>
<body>
  """ + _NAV + """
  <h1>📇 Acme Sales — CRM</h1>
  <p class="hint">Agent view. <code>agent.js</code> only opens a WebSocket to the
  gateway with a short-lived JWT (minted at <code>/agent/token</code>) and renders
  the in-call panel — it has <strong>no Tring API access and cannot place calls</strong>.
  The <em>Call</em> buttons below are the CRM's own; they POST a customer <em>id</em>
  (never a number) to this backend's <code>/agent/originate</code>, which resolves
  the number and calls Tring <code>POST /v1/calls</code>. So the agent's browser
  never sees the dialed number — they just get connected.</p>

  {% if agent_err %}<p class="warn">⚠ {{ agent_err }} — calls won't work until this is fixed.</p>
  {% else %}<p class="you">Acting as agent <code>{{ agent_id }}</code> · numbers stay server-side</p>{% endif %}

  <table>
    <thead><tr><th>Customer</th><th></th></tr></thead>
    <tbody>
      {% for c in customers %}
      <tr><td>{{ c.name }}</td>
        <td><button onclick="callCustomer('{{ c.id }}', this)">📞 Call</button></td></tr>
      {% endfor %}
    </tbody>
  </table>
  <p id="crmStatus" class="hint"></p>

  <script>
    // The CRM's OWN button. It sends a customer id to our backend — not a phone
    // number — so the number never touches the browser. The backend originates;
    // the agent's leg then auto-answers and the agent.js panel shows the label.
    async function callCustomer(id, btn) {
      const st = document.getElementById('crmStatus');
      btn.disabled = true; st.textContent = 'Connecting…';
      try {
        const r = await fetch('/agent/originate', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ customer: id }),
        });
        st.textContent = r.ok ? 'Ringing your handset…' : ('Failed: ' + (await r.text()));
      } catch (e) {
        st.textContent = 'Error: ' + e;
      } finally {
        btn.disabled = false;
      }
    }
  </script>

  <!-- Agent widget: WS-only media client. Just data-token-url — the token
       endpoint returns the gateway URL (+ ICE) too, so no data-gateway-url and
       no data-api-base. It can't originate; it only rings/answers. -->
  <script src="{{ api_base }}/sdk/agent.js"
          data-token-url="/agent/token"></script>
</body>"""
)


if __name__ == "__main__":
    if not API_KEY:
        print("WARNING: TRING_API_KEY is not set — the trusted-support and agent demos will 500.")
    app.run(host="127.0.0.1", port=5000, debug=True)

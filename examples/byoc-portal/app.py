"""
Demo: a logged-in support portal that places web calls with a TRUSTED caller
identity, using Tring's bring-your-own-credentials flow.

The point: the browser never holds the Tring API key and never calls the
public guest mint, so the caller identity can't be spoofed.

    browser (logged in)  ──GET /guest-credentials──►  THIS Flask backend
                                                       │  (knows the logged-in
                                                       │   client from session)
                                                       ▼
                         Tring  POST /v1/guest-credentials
                           Authorization: Bearer <TRING_API_KEY>
                           { "client_ref": "<verified client id>" }
                                                       │
                    ◄─── gateway call token + url ─────┘
    browser uses the creds with the Tring voice widget → places the call.

The Tring call log then shows the client (client_ref), never `guest_*`.

Run:
    pip install -r requirements.txt
    export TRING_API_BASE=http://localhost:8085
    export TRING_API_KEY=tk_live_...           # your tenant's API key
    export TRING_FLOW_ID=<flow-uuid>           # optional; else tenant support flow
    python app.py
    # open http://localhost:5000  (log in as alice / demo)
"""

import json
import os
import secrets
import urllib.error
import urllib.request

from flask import Flask, abort, jsonify, redirect, render_template_string, request, session

# ── Tring connection (server-side only — the API key never reaches the browser)
API_BASE = os.environ.get("TRING_API_BASE", "http://localhost:8085").rstrip("/")
API_KEY = os.environ.get("TRING_API_KEY", "")
FLOW_ID = os.environ.get("TRING_FLOW_ID", "")  # optional; falls back to tenant support flow

app = Flask(__name__)
app.secret_key = os.environ.get("FLASK_SECRET", secrets.token_hex(16))

# Stand-in for your real auth / customer records. Each "user" maps to the
# client identity you want attributed on the call log.
DEMO_USERS = {
    "alice": {"password": "demo", "client_ref": "cust_ACME_42", "name": "Alice — ACME Corp"},
    "bob": {"password": "demo", "client_ref": "cust_GLOBEX_7", "name": "Bob — Globex"},
}


def mint_credentials(client_ref: str):
    """Call Tring's authenticated mint with the verified client id. Returns
    (http_status, json_body). The API key authorizes it, so Tring trusts
    client_ref and stamps it on the call."""
    body = {"client_ref": client_ref}
    if FLOW_ID:
        body["flow_id"] = FLOW_ID
    req = urllib.request.Request(
        f"{API_BASE}/v1/guest-credentials",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {API_KEY}"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        # Surface Tring's {"error": "..."} body (e.g. "guest calling not enabled").
        try:
            return e.code, json.load(e)
        except Exception:
            return e.code, {"error": f"mint failed ({e.code})"}
    except urllib.error.URLError as e:
        return 502, {"error": f"cannot reach Tring at {API_BASE}: {e.reason}"}


@app.get("/")
def home():
    user = session.get("user")
    if not user:
        return render_template_string(LOGIN_HTML)
    u = DEMO_USERS[user]
    return render_template_string(
        PORTAL_HTML, name=u["name"], client_ref=u["client_ref"], api_base=API_BASE
    )


@app.post("/login")
def login():
    username = request.form.get("username", "")
    password = request.form.get("password", "")
    u = DEMO_USERS.get(username)
    if not u or u["password"] != password:
        return render_template_string(LOGIN_HTML, error="Invalid login (try alice / demo)."), 401
    session["user"] = username
    return redirect("/")


@app.post("/logout")
def logout():
    session.clear()
    return redirect("/")


@app.get("/guest-credentials")
def guest_credentials():
    """The widget's data-credentials-url target. Same-origin + cookie-authed,
    so only a logged-in visitor can mint — and only ever as THEIR client_ref."""
    user = session.get("user")
    if not user:
        abort(401)
    if not API_KEY:
        return jsonify({"error": "server misconfigured: TRING_API_KEY not set"}), 500
    status, payload = mint_credentials(DEMO_USERS[user]["client_ref"])
    return jsonify(payload), status


_STYLE = """
  body { font: 15px/1.6 system-ui, sans-serif; max-width: 640px; margin: 3rem auto; padding: 0 1rem; color: #111827; }
  h1 { margin: 0 0 .25rem; }
  .hint { color: #6b7280; font-size: 13px; margin: 0 0 1.5rem; }
  .card { border: 1px solid #e5e7eb; border-radius: 12px; padding: 1.5rem; margin-top: 1rem; }
  .you { background: #ecfdf5; border: 1px solid #a7f3d0; border-radius: 8px; padding: .75rem 1rem; font-size: 13px; }
  code { background: rgba(0,0,0,.06); padding: .1rem .3rem; border-radius: 3px; }
  input { font: inherit; padding: .45rem .5rem; width: 100%; border: 1px solid #d1d5db; border-radius: 6px; box-sizing: border-box; margin-bottom: .5rem; }
  button { font: inherit; padding: .5rem 1rem; background: #2563eb; color: #fff; border: 0; border-radius: 6px; cursor: pointer; }
  button.link { background: none; color: #6b7280; padding: .25rem 0; text-decoration: underline; }
"""

LOGIN_HTML = (
    """
<!doctype html><meta charset="utf-8"><title>Acme — Support sign in</title>
<style>"""
    + _STYLE
    + """</style>
<body>
  <h1>🛟 Acme Help Center</h1>
  <p class="hint">Sign in to talk to support. Once you're logged in, your call is
  placed with <strong>your verified identity</strong> — the call log shows who
  you are, not an anonymous guest.</p>
  <div class="card">
    {% if error %}<p style="color:#c00">{{ error }}</p>{% endif %}
    <form method="post" action="/login">
      <input name="username" type="text" placeholder="username — alice or bob" autocomplete="username">
      <input name="password" type="password" placeholder="password — demo" autocomplete="current-password">
      <button>Sign in</button>
    </form>
  </div>
</body>
"""
)

PORTAL_HTML = (
    """
<!doctype html><meta charset="utf-8"><title>Acme — Support</title>
<style>"""
    + _STYLE
    + """</style>
<body>
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

  <!-- Tring visitor widget. The bundle loads from the Tring API; the gateway
       connection details + a client_ref-bearing call token come from THIS app
       (data-credentials-url), minted server-side with the tenant API key. -->
  <script src="{{ api_base }}/sdk/voice.js"
          data-credentials-url="/guest-credentials"
          data-button-label="Call support"
          data-title="Acme Support"></script>
</body>
"""
)


if __name__ == "__main__":
    if not API_KEY:
        print("WARNING: TRING_API_KEY is not set — /guest-credentials will 500.")
    app.run(host="127.0.0.1", port=5000, debug=True)

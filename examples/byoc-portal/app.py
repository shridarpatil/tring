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
                         ◄──────── SIP credentials ────┘
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


LOGIN_HTML = """
<!doctype html><meta charset="utf-8"><title>Support portal — login</title>
<body style="font-family:system-ui;max-width:24rem;margin:4rem auto">
  <h1>Support portal</h1>
  <p style="color:#666">Demo of Tring bring-your-own-credentials calling.</p>
  {% if error %}<p style="color:#c00">{{ error }}</p>{% endif %}
  <form method="post" action="/login">
    <p><input name="username" placeholder="username (alice / bob)" style="width:100%;padding:.5rem"></p>
    <p><input name="password" type="password" placeholder="password (demo)" style="width:100%;padding:.5rem"></p>
    <button style="padding:.5rem 1rem">Log in</button>
  </form>
</body>
"""

PORTAL_HTML = """
<!doctype html><meta charset="utf-8"><title>Support portal</title>
<body style="font-family:system-ui;max-width:32rem;margin:4rem auto">
  <h1>Welcome, {{ name }}</h1>
  <p style="color:#666">
    You're logged in. Click <b>Call support</b> below — the call is placed with
    your verified identity <code>{{ client_ref }}</code>, minted by this backend
    (never the browser). It shows in Tring's call log as
    <code>{{ client_ref }}</code>, not <code>guest_…</code>.
  </p>
  <form method="post" action="/logout"><button style="padding:.3rem .8rem">Log out</button></form>

  <!-- Tring visitor widget. The bundle loads from the Tring API; credentials
       come from THIS app (data-credentials-url), which mints them server-side. -->
  <script src="{{ api_base }}/sdk/voice.js"
          data-credentials-url="/guest-credentials"
          data-button-label="Call support"
          data-title="Support"></script>
</body>
"""


if __name__ == "__main__":
    if not API_KEY:
        print("WARNING: TRING_API_KEY is not set — /guest-credentials will 500.")
    app.run(host="127.0.0.1", port=5000, debug=True)

---
title: Single sign-on (OIDC)
description: Let console users sign in with Google Workspace, Microsoft Entra, Okta, Keycloak or any OpenID Connect provider, and optionally require it for an email domain.
---

Tring console users can sign in through your identity provider (IdP) using OpenID Connect (authorization code flow with PKCE). The login page asks for an email first; if the email's domain belongs to an enabled SSO provider, the browser is sent to the IdP, otherwise the password field appears.

SSO only authenticates. **Users are still created by an admin** (Users page or the [Users API](/tring/api-reference/users/)); there is no auto-provisioning. A person who signs in at the IdP but has no Tring user with that email sees "No Tring account for this email, ask your admin".

## Prerequisites

- **Redis.** The sign-in state lives in Redis. Without it the login page falls back to the password form (and a require-SSO domain then only admits admins, see below).
- **`TRING_CONSOLE_PUBLIC_URL`.** The browser-reachable base URL of the api/console, for example `https://tring.example.com`. It defaults to `http://localhost:8085`. The redirect URI you register at the IdP is:

  ```
  {TRING_CONSOLE_PUBLIC_URL}/v1/console/sso/callback
  ```

  The console shows the exact value, with a copy button, next to **Redirect URI** in **Settings > Single sign-on**.
- **Private IdPs.** Tring fetches the IdP's discovery document, keys and token endpoint through an SSRF-guarded client. A self-hosted IdP (for example Keycloak) on a private network is blocked unless its range is listed in `TRING_OUTBOUND_ALLOW_CIDRS`, for example `10.0.0.0/8`.

## Add a provider in Tring

In the console go to **Settings > Single sign-on** and choose **Add provider**:

| Field | Value |
|---|---|
| **Name** | A label, for example `Okta` |
| **Issuer URL** | The IdP's issuer (see each provider below). Tring appends `/.well-known/openid-configuration` |
| **Client ID** / **Client secret** | From the OIDC client you create at the IdP. The secret is stored encrypted and never shown again; leave it blank when editing to keep it |
| **Email domains** | Comma-separated, for example `acme.com, acme.io`. Matching is exact: `acme.com` does not cover `mail.acme.com` |
| **Enabled** | Off keeps the provider configured but unused |
| **Require SSO (admins can still use a password)** | See [Requiring SSO](#requiring-sso) |

Request the scopes `openid email` at the IdP (Tring asks for exactly those). Tring signs a user in only if the ID token carries an `email` whose domain is one of the provider's domains, `email_verified` is `true`, and a Tring user with that email exists in the same tenant.

## Provider setup

Register the redirect URI above as a **web** application with a client secret in each case.

### Google Workspace

1. In Google Cloud Console, go to **APIs & Services > OAuth consent screen**, choose **Internal** (restricts sign-in to your Workspace), and complete it.
2. **Credentials > Create credentials > OAuth client ID**, type **Web application**. Add the redirect URI under **Authorized redirect URIs**.
3. Issuer URL: `https://accounts.google.com`

**Require a Google Workspace account.** Anyone can create a personal Google account on a work email address they can receive mail at, and Google marks that email as verified. Workspace-managed accounts carry an `hd` (hosted domain) claim naming the Workspace domain; personal accounts do not. Turn on **Require a Google Workspace account (Google only)** on the provider and Tring only accepts a sign-in whose `hd` is one of the provider's email domains. It matches the provider's list rather than the user's own address because Google may report the Workspace's primary domain for a user on a secondary domain, so list both. Failed checks show the generic "sign-in failed" message on the login page; the reason (`hosted domain mismatch`) is in the server log.

- Shared or **External** OAuth client (any Google account can reach it): strongly recommended.
- Per-company **Internal** client: optional extra insurance.
- Leave it off for non-Google providers; they do not send `hd`, so every sign-in would be refused.

### Microsoft Entra ID

1. In the Entra admin center, **App registrations > New registration**. Platform **Web**, redirect URI as above.
2. **Certificates & secrets > New client secret**. Copy the secret **value**. The Client ID is the **Application (client) ID**.
3. Issuer URL: `https://login.microsoftonline.com/<directory-tenant-id>/v2.0`

:::caution[Entra and `email_verified`]
Entra often omits the `email_verified` claim. Tring requires it to be `true` and does not make an exception, so those sign-ins fail with **Single sign-on failed**. If this happens, configure Entra to emit it (for example as an optional claim on the ID token, where your tenant allows it) or use a different IdP for that domain. The cause is in the api log (`email not verified`).
:::

### Okta

1. **Applications > Create App Integration**, sign-in method **OIDC**, application type **Web Application**.
2. Add the redirect URI under **Sign-in redirect URIs** and assign the people or groups who may sign in.
3. Issuer URL: your org URL, `https://<your-org>.okta.com` (the org authorization server; for a custom authorization server use `https://<your-org>.okta.com/oauth2/<server-id>`).

### Keycloak

1. In your realm, **Clients > Create client**, client type **OpenID Connect**, enable **Client authentication**, standard flow only.
2. Add the redirect URI under **Valid redirect URIs**. The secret is on the **Credentials** tab.
3. Issuer URL: `https://<host>/realms/<realm>`
4. Keycloak marks `email_verified` per user; make sure it is set (or verify emails) for people who will sign in. If Keycloak is on a private network, set `TRING_OUTBOUND_ALLOW_CIDRS`.

## Signing in

1. The user enters their email and selects **Continue**, signs in at the IdP, and lands back in Tring.
2. On success the callback redirects to `/login#sso_code=...`. The code is single use, valid for 60 seconds, and travels in the URL fragment so it never reaches server logs. The console exchanges it for the session.
3. The flow is bound to the browser by an HttpOnly cookie (`tring_sso`). The user must **start and finish in the same browser, and on the same host as `TRING_CONSOLE_PUBLIC_URL`**. If that URL is `http://localhost:8085`, browse to `localhost`, not `127.0.0.1`, or the cookie will not come back and the sign-in fails with "Sign-in took too long, try again".

## The sign-in button

When exactly **one** SSO provider is enabled on the install, the login page shows a **Sign in with <provider name>** button above the form, then an "or" divider and the usual email and password fields. The button starts SSO without asking for an email; every check after the IdP redirect is the same (verified ID token, email domain owned by the provider, an existing Tring user).

- The button needs Redis, like SSO itself. With no enabled provider, two or more, or no Redis, the page is the email-first form described above.
- The provider's **name is shown publicly** on the login page, so don't put anything sensitive in it.
- Adding a second enabled provider (or disabling the only one) makes the button disappear; people then sign in by email as before.
- Tring sends no `login_hint` for the button, so Google users signed into several accounts see Google's account picker.

The login page shows only three failure messages, on purpose: "Sign-in took too long, try again", "No Tring account for this email, ask your admin" and "Single sign-on failed". The specific reason is in the api log.

## Users without a password

When you create a user (**Users > Add** or `POST /v1/users`) whose email is on a domain with an SSO provider, the password may be left blank: the user has no password and can only sign in through SSO. For any other domain a password (at least 8 characters) is still required. You can later set one with **Set login** or **Reset password**.

## Requiring SSO

With **Require SSO** on, password sign-in is refused for everyone on the provider's domains, with "this account must sign in with SSO". **Admins are exempt**, so they remain break-glass accounts if the IdP is down or misconfigured: on the login page, **Sign in with password** shows the password field for them. Supervisors and agents have no such way around it.

If Redis is unavailable, or the IdP's discovery fails, the login page falls back to the password form; require-SSO still applies, so only admins get in.

Email domains are global across the install, not per tenant. On a multi-tenant install one tenant can claim a domain first, and people from another tenant on that domain are then routed to the wrong IdP. Sign-in still requires a user in the provider's own tenant, so this is not an account takeover, but treat adding providers as a trusted-admin action.

Deleting a provider returns its domains to password sign-in. Users that had no password cannot sign in until an admin sets one.

## Sessions and revocation

A console session is a token valid for 12 hours, with no refresh. SSO does not change that.

- Disabling or deleting a person at the IdP stops **new** sign-ins immediately, but an existing Tring session keeps working until it expires.
- Turning on **Require SSO** does not end existing password sessions; they last up to 12 hours. Use **Clear login** to end one immediately.
- To cut access immediately, use **Clear login** on the user in Tring. Roles and logins are checked on every request, so it takes effect at once.

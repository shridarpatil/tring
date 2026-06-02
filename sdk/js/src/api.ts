// Thin REST client for the Tring backend. Only the two endpoints the SDK
// needs: SIP credential fetch (during init) and call origination.

import type {
  GuestCredentialsResponse,
  SipCredentialsResponse,
} from "./types";

export class ApiClient {
  constructor(
    private readonly base: string,
    // Optional: the guest flow has no token (it authenticates with a
    // public key instead), so the guest endpoint below ignores it.
    private readonly token = "",
  ) {}

  /**
   * Mints an anonymous, flow-pinned guest SIP identity for a website
   * visitor. Public endpoint — authorized by the tenant's *public* widget
   * key, not a bearer token. Returns SIP creds plus the IVR dial target.
   */
  async fetchGuestCredentials(
    publicKey: string,
    flowId?: string,
  ): Promise<GuestCredentialsResponse> {
    const r = await fetch(`${this.base}/v1/sdk/guest-credentials`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ public_key: publicKey, flow_id: flowId }),
    });
    if (!r.ok) {
      // Surface the server's plain-English {error} (e.g. "guest calling is
      // not enabled…") rather than a raw "failed: 400 {…}" blob.
      let msg = `request failed (${r.status})`;
      try {
        const j = await r.json();
        if (j && typeof j.error === "string") msg = j.error;
      } catch {
        /* non-JSON error body */
      }
      throw new Error(msg);
    }
    return r.json();
  }

  async fetchCredentials(): Promise<SipCredentialsResponse> {
    const r = await fetch(`${this.base}/v1/sdk/credentials`, {
      headers: { Authorization: `Bearer ${this.token}` },
    });
    if (!r.ok) {
      throw new Error(
        `tring credentials fetch failed: ${r.status} ${await r.text()}`,
      );
    }
    return r.json();
  }

  /**
   * Triggers an outbound call. Uses the JWT-scoped /v1/sdk/calls endpoint
   * so the browser never needs the tenant API key. The agent identity
   * comes from the JWT subject server-side; the request body only carries
   * the destination.
   */
  async originate(to: string): Promise<{ id: string; fs_uuid: string }> {
    const r = await fetch(`${this.base}/v1/sdk/calls`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${this.token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ to }),
    });
    if (!r.ok) {
      throw new Error(
        `tring originate failed: ${r.status} ${await r.text()}`,
      );
    }
    return r.json();
  }
}

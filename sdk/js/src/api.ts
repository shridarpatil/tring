// Thin REST client for the Tring backend. Only the two endpoints the SDK
// needs: SIP credential fetch (during init) and call origination.

import type { SipCredentialsResponse } from "./types";

export class ApiClient {
  constructor(
    private readonly base: string,
    private readonly token: string,
  ) {}

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

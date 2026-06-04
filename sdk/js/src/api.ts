// Thin REST client for the Tring backend. The only endpoint the SDK needs is
// call origination — the agent's browser peers with the media gateway over a
// WebSocket for everything else (ADR-001), so there's no SIP credential fetch.

export class ApiClient {
  constructor(
    private readonly base: string,
    private readonly token = "",
  ) {}

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
      throw new Error(`tring originate failed: ${r.status} ${await r.text()}`);
    }
    return r.json();
  }
}

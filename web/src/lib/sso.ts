// Pure helpers for the SSO login flow and the SSO settings form, kept free of
// Vue so they are unit-testable.

/** What the server's /login redirect leaves in the URL fragment after a successful IdP round trip. */
export function parseSsoFragment(hash: string): { code: string; redirect: string } | null {
  const p = new URLSearchParams(hash.replace(/^#/, ""));
  const code = p.get("sso_code");
  return code ? { code, redirect: p.get("redirect") ?? "" } : null;
}

const ssoErrors: Record<string, string> = {
  expired: "Sign-in took too long, try again",
  no_account: "No Tring account for this email — ask your admin",
  failed: "Single sign-on failed",
};

/** Message for a /login?sso_error=… code; "" when absent, a generic message for an unknown code. */
export function ssoErrorMessage(code: unknown): string {
  if (typeof code !== "string" || !code) return "";
  return ssoErrors[code] ?? ssoErrors.failed;
}

/** Splits the comma/space/newline separated domain input into trimmed, lower-cased, de-duplicated domains. */
export function splitDomains(input: string): string[] {
  const out: string[] = [];
  for (const d of input.split(/[\s,]+/)) {
    const v = d.trim().toLowerCase().replace(/^@/, "");
    if (v && !out.includes(v)) out.push(v);
  }
  return out;
}

/** Confirm text for deleting a provider; warns when users would be locked out. */
export function deleteProviderMessage(name: string, ssoOnlyUsers: number): string {
  const base = `Delete ${name}? People on its domains sign in with a password again.`;
  if (ssoOnlyUsers <= 0) return base;
  const n = ssoOnlyUsers === 1 ? "1 user on these domains has" : `${ssoOnlyUsers} users on these domains have`;
  return `${base} ${n} no password and won't be able to sign in.`;
}

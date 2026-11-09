// Pure helpers for the admin Users page, kept free of Vue so they are
// unit-testable; the server enforces the same rules.
import type { Role } from "./roles.ts";

export const MIN_PASSWORD_LEN = 8;

export const roleOptions: { value: Role; label: string }[] = [
  { value: "admin", label: "Admin" },
  { value: "supervisor", label: "Supervisor" },
  { value: "agent", label: "Agent" },
];

export function roleLabel(r: string): string {
  return roleOptions.find((o) => o.value === r)?.label ?? r;
}

export interface NewUserForm {
  email: string;
  password: string;
  role: string;
  display_name: string;
  sip_username: string;
}

/** Blank email and password = a CRM-only agent (SIP identity, no console login). */
export function isAgentOnly(f: NewUserForm): boolean {
  return f.email.trim() === "" && f.password === "";
}

/** First problem with the create form, or "" when it can be submitted. */
export function validateNewUser(f: NewUserForm): string {
  if (isAgentOnly(f)) return f.sip_username.trim() ? "" : "Enter a SIP username, or an email";
  if (!f.email.trim().includes("@")) return "Enter a valid email";
  if (f.password !== "" && f.password.length < MIN_PASSWORD_LEN) return `Password must be at least ${MIN_PASSWORD_LEN} characters`;
  if (!roleOptions.some((o) => o.value === f.role)) return "Pick a role";
  return "";
}

/** Problem with a set-login / reset-password form, or "". */
export function validateLogin(email: string, password: string): string {
  if (!email.trim().includes("@")) return "Enter a valid email";
  if (password !== "" && password.length < MIN_PASSWORD_LEN) return `Password must be at least ${MIN_PASSWORD_LEN} characters`;
  return "";
}

export interface EditUserForm {
  display_name: string;
  email: string;
  role: string;
  sip_username: string;
}

// Mirrors the server's sipUsernameRe (internal/api/esl_inputs.go).
const SIP_USERNAME_RE = /^[A-Za-z0-9._@+-]{1,64}$/;

/**
 * Whether an edited SIP username renames the user. Only whitespace is trimmed:
 * case is significant (CRM ids are stored case-preserved), so a case change
 * is a rename.
 */
export function sipChanged(saved: string, edited: string): boolean {
  return edited.trim() !== saved;
}

/**
 * Fields of the edit dialog that differ from the saved user, or a problem.
 * Emptying the email of a user with a login is refused: the server treats
 * that as "clear login" (and ignores every other field), which has its own
 * confirmed action. A login-less user's email is not editable here (setting
 * one without a password would half-create a login), so it is never sent.
 */
export function buildUserPatch(
  cur: EditUserForm & { can_login: boolean },
  f: EditUserForm,
): { body: Record<string, string>; problem: string } {
  const body: Record<string, string> = {};
  if (f.display_name.trim() !== cur.display_name) body.display_name = f.display_name.trim();
  if (cur.can_login) {
    const email = f.email.trim();
    if (email === "") return { body, problem: 'Email can\'t be empty; use "Clear login" to remove the login' };
    if (!email.includes("@")) return { body, problem: "Enter a valid email" };
    if (email.toLowerCase() !== cur.email.toLowerCase()) body.email = email;
  }
  if (!roleOptions.some((o) => o.value === f.role)) return { body, problem: "Pick a role" };
  if (f.role !== cur.role) body.role = f.role;
  if (sipChanged(cur.sip_username, f.sip_username)) {
    const sip = f.sip_username.trim();
    if (!SIP_USERNAME_RE.test(sip)) {
      return { body, problem: "SIP username may only contain letters, digits and . _ @ + - (max 64)" };
    }
    body.sip_username = sip;
  }
  return { body, problem: "" };
}

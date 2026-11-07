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
  if (isAgentOnly(f)) return f.sip_username.trim() ? "" : "Enter a SIP username, or an email and password";
  if (!f.email.trim().includes("@")) return "Enter a valid email";
  if (f.password.length < MIN_PASSWORD_LEN) return `Password must be at least ${MIN_PASSWORD_LEN} characters`;
  if (!roleOptions.some((o) => o.value === f.role)) return "Pick a role";
  return "";
}

/** Problem with a set-login / reset-password form, or "". */
export function validateLogin(email: string, password: string): string {
  if (!email.trim().includes("@")) return "Enter a valid email";
  if (password.length < MIN_PASSWORD_LEN) return `Password must be at least ${MIN_PASSWORD_LEN} characters`;
  return "";
}

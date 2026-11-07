// Role helpers for the role-aware console. Pure (no Vue/store imports) so the
// routing and nav rules are unit-testable; the server enforces the same rules.

export type Role = "admin" | "supervisor" | "agent";

export interface MeUser {
  id: string;
  display_name: string;
  email: string;
  role: Role;
  sip_username: string;
  teams: { name: string; is_manager: boolean }[];
}

/** Where a user lands after sign-in: admins the dashboard (/admin/), others their own page (/agent). */
export function homeRoute(u: MeUser): "dashboard" | "me" {
  return u.role === "admin" ? "dashboard" : "me";
}

/** Admin-only routes are marked meta.admin; everything else is open to all roles. */
export function canOpen(u: MeUser, route: { meta?: { admin?: boolean } }): boolean {
  return !route.meta?.admin || u.role === "admin";
}

/** Supervisors manage every team; agents only the teams they are a manager of. */
export function canManageTeam(u: MeUser, team: string): boolean {
  if (u.role === "admin" || u.role === "supervisor") return true;
  return (u.teams ?? []).some((t) => t.name === team && t.is_manager);
}

/**
 * Route guard decision. A missing user (refresh failed, non-401) is treated as
 * non-admin so admin pages fail closed instead of being skipped.
 */
export function guardTarget(u: MeUser | null, route: { meta?: { admin?: boolean } }): "ok" | "me" {
  if (!route.meta?.admin) return "ok";
  return u && canOpen(u, route) ? "ok" : "me";
}

/**
 * The post-login redirect target, if it is a page of this app (/admin or
 * /agent). Anything else — "//evil.example", "https://…", the login page
 * itself — is dropped so the login form can't be turned into an open redirect.
 */
export function safeRedirect(r: unknown): string | null {
  return typeof r === "string" && /^\/(admin|agent)(\/|\?|$)/.test(r) ? r : null;
}

/**
 * What the /agent dashboard shows: supervisors everything (the tenant-wide
 * stream), team managers their managed teams, plain agents their own teams and
 * calls. The server scopes the data the same way; this only picks the source
 * and the controls.
 */
export type DashboardScope = "all" | "managed" | "own";

export function dashboardScope(u: MeUser): DashboardScope {
  if (u.role === "admin" || u.role === "supervisor") return "all";
  return (u.teams ?? []).some((t) => t.is_manager) ? "managed" : "own";
}

/**
 * Whether to offer Listen on a live call. A manager is sent only managed teams'
 * calls plus their own, and can't supervise a call they are on; the server
 * re-checks every supervise request.
 */
export function canSuperviseCall(u: MeUser, c: { agent_id?: string }): boolean {
  const scope = dashboardScope(u);
  if (scope === "all") return true;
  return scope === "managed" && c.agent_id !== u.id;
}

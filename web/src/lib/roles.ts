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

/** Where a user lands after sign-in: admins get the dashboard, others their own page. */
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

import test from "node:test";
import assert from "node:assert/strict";
import { canManageTeam, canOpen, homeRoute, type MeUser } from "./roles.ts";

const mk = (role: MeUser["role"], teams: MeUser["teams"] = []): MeUser => ({
  id: "u1",
  display_name: "U",
  email: "u@example.com",
  role,
  sip_username: "u1",
  teams,
});

test("homeRoute: admin -> dashboard, others -> me", () => {
  assert.equal(homeRoute(mk("admin")), "dashboard");
  assert.equal(homeRoute(mk("agent")), "me");
  assert.equal(homeRoute(mk("supervisor")), "me");
});

test("canOpen: admin-only routes are closed to non-admins", () => {
  assert.equal(canOpen(mk("agent"), { meta: { admin: true } }), false);
  assert.equal(canOpen(mk("supervisor"), { meta: { admin: true } }), false);
  assert.equal(canOpen(mk("admin"), { meta: { admin: true } }), true);
  assert.equal(canOpen(mk("agent"), {}), true);
});

test("canManageTeam: supervisors all teams, agents only teams they manage", () => {
  assert.equal(canManageTeam(mk("supervisor"), "x"), true);
  const a = mk("agent", [
    { name: "A", is_manager: true },
    { name: "B", is_manager: false },
  ]);
  assert.equal(canManageTeam(a, "A"), true);
  assert.equal(canManageTeam(a, "B"), false);
  assert.equal(canManageTeam(a, "C"), false);
});

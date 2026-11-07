import test from "node:test";
import assert from "node:assert/strict";
import { canManageTeam, canOpen, guardTarget, homeRoute, safeRedirect, type MeUser } from "./roles.ts";

const mk = (role: MeUser["role"], teams: MeUser["teams"] = []): MeUser => ({
  id: "u1",
  display_name: "U",
  email: "u@example.com",
  role,
  sip_username: "u1",
  teams,
});

test("homeRoute: admin -> dashboard (/admin/), others -> me (/agent)", () => {
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

test("guardTarget: fails closed for admin routes", () => {
  const r = { meta: { admin: true } };
  assert.equal(guardTarget(null, r), "me");
  assert.equal(guardTarget(mk("agent"), r), "me");
  assert.equal(guardTarget(mk("admin"), r), "ok");
  assert.equal(guardTarget(null, {}), "ok");
});

test("safeRedirect: only pages of this app", () => {
  for (const ok of ["/admin/", "/admin/calls/abc?view=log", "/agent", "/agent?x=1", "/agent/teams/sales"]) {
    assert.equal(safeRedirect(ok), ok);
  }
  for (const bad of ["//evil.example", "https://evil.example", "/login", "/", "/administrator", "/agents", "", undefined, ["/agent"]]) {
    assert.equal(safeRedirect(bad), null);
  }
});

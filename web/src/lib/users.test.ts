import test from "node:test";
import assert from "node:assert/strict";
import { isAgentOnly, roleLabel, validateLogin, validateNewUser } from "./users.ts";

const ok = { email: "a@example.com", password: "longenough", role: "agent", display_name: "", sip_username: "" };

test("roleLabel", () => {
  assert.equal(roleLabel("supervisor"), "Supervisor");
  assert.equal(roleLabel("weird"), "weird");
});

test("validateNewUser", () => {
  assert.equal(validateNewUser(ok), "");
  assert.match(validateNewUser({ ...ok, email: "nope" }), /email/);
  assert.match(validateNewUser({ ...ok, password: "short" }), /8/);
  assert.match(validateNewUser({ ...ok, role: "root" }), /role/);
  assert.match(validateNewUser({ ...ok, email: "a@example.com", password: "" }), /8/);
  assert.match(validateNewUser({ ...ok, email: "", password: "longenough" }), /email/);
});

test("agent-only create", () => {
  const blank = { ...ok, email: "", password: "", sip_username: "" };
  assert.match(validateNewUser(blank), /SIP/);
  assert.equal(validateNewUser({ ...blank, sip_username: "alice" }), "");
  assert.equal(isAgentOnly({ ...blank, sip_username: "alice" }), true);
  assert.equal(isAgentOnly(ok), false);
});

test("validateLogin", () => {
  assert.equal(validateLogin("a@example.com", "longenough"), "");
  assert.match(validateLogin("", "longenough"), /email/);
  assert.match(validateLogin("a@example.com", "x"), /8/);
});

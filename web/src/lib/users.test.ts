import test from "node:test";
import assert from "node:assert/strict";
import { buildUserPatch, isAgentOnly, roleLabel, sipChanged, validateLogin, validateNewUser } from "./users.ts";

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
  // Empty password = SSO user; the server checks the domain.
  assert.equal(validateNewUser({ ...ok, email: "a@example.com", password: "" }), "");
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
  assert.equal(validateLogin("a@example.com", ""), "");
});

test("buildUserPatch", () => {
  const cur = { display_name: "Al", email: "a@example.com", role: "agent", sip_username: "al", can_login: true };
  const same = { display_name: "Al", email: "a@example.com", role: "agent", sip_username: "al" };
  assert.deepEqual(buildUserPatch(cur, same), { body: {}, problem: "" });
  assert.deepEqual(buildUserPatch(cur, { ...same, display_name: " Bo ", role: "admin" }).body, { display_name: "Bo", role: "admin" });
  assert.deepEqual(buildUserPatch(cur, { ...same, email: "A@Example.com" }).body, {});
  assert.deepEqual(buildUserPatch(cur, { ...same, email: "b@example.com" }).body, { email: "b@example.com" });
  assert.match(buildUserPatch(cur, { ...same, email: " " }).problem, /Clear login/);
  assert.match(buildUserPatch(cur, { ...same, email: "nope" }).problem, /email/);
  assert.match(buildUserPatch(cur, { ...same, role: "root" }).problem, /role/);
  const nologin = { display_name: "", email: "", role: "agent", sip_username: "al", can_login: false };
  assert.deepEqual(buildUserPatch(nologin, { display_name: "X", email: "z@example.com", role: "agent", sip_username: "al" }).body, { display_name: "X" });
});

test("buildUserPatch sip_username", () => {
  const cur = { display_name: "Al", email: "a@example.com", role: "agent", sip_username: "al", can_login: true };
  const same = { display_name: "Al", email: "a@example.com", role: "agent", sip_username: "al" };
  // Sent only when the trimmed value differs; case is significant.
  assert.deepEqual(buildUserPatch(cur, { ...same, sip_username: " al " }).body, {});
  assert.deepEqual(buildUserPatch(cur, { ...same, sip_username: " Alan " }).body, { sip_username: "Alan" });
  const mixed = { ...cur, sip_username: "CRM_User42" };
  const mixedSame = { ...same, sip_username: "CRM_User42" };
  assert.deepEqual(buildUserPatch(mixed, mixedSame), { body: {}, problem: "" });
  assert.deepEqual(buildUserPatch(mixed, { ...mixedSame, display_name: "Bo" }).body, { display_name: "Bo" });
  assert.deepEqual(buildUserPatch(mixed, { ...mixedSame, sip_username: "crm_user42" }).body, { sip_username: "crm_user42" });
  assert.match(buildUserPatch(cur, { ...same, sip_username: "" }).problem, /SIP username/);
  assert.match(buildUserPatch(cur, { ...same, sip_username: "bad name" }).problem, /SIP username/);
  assert.match(buildUserPatch(cur, { ...same, sip_username: "x".repeat(65) }).problem, /SIP username/);
  assert.equal(buildUserPatch(cur, { ...same, sip_username: "a.l_1@x+y-z" }).problem, "");
});

test("sipChanged", () => {
  assert.equal(sipChanged("al", " al "), false);
  assert.equal(sipChanged("al", "alan"), true);
  assert.equal(sipChanged("CRM_User42", "CRM_User42"), false);
  assert.equal(sipChanged("CRM_User42", "crm_user42"), true);
});

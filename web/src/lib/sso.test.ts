import test from "node:test";
import assert from "node:assert/strict";
import { deleteProviderMessage, splitDomains, ssoButtonName, ssoCode, ssoErrorMessage } from "./sso.ts";

test("ssoCode", () => {
  assert.equal(ssoCode("abc"), "abc");
  assert.equal(ssoCode(undefined), "");
  assert.equal(ssoCode(["a", "b"]), "");
});

test("ssoErrorMessage", () => {
  assert.match(ssoErrorMessage("expired"), /too long/);
  assert.match(ssoErrorMessage("no_account"), /ask your admin/);
  assert.equal(ssoErrorMessage("failed"), "Single sign-on failed");
  assert.equal(ssoErrorMessage("weird"), "Single sign-on failed");
  assert.equal(ssoErrorMessage(undefined), "");
  assert.equal(ssoErrorMessage(["a"]), "");
});

test("splitDomains", () => {
  assert.deepEqual(splitDomains("Example.com, corp.example.com"), ["example.com", "corp.example.com"]);
  assert.deepEqual(splitDomains(" @a.com\nb.com  a.com,, "), ["a.com", "b.com"]);
  assert.deepEqual(splitDomains(""), []);
});

test("deleteProviderMessage", () => {
  assert.doesNotMatch(deleteProviderMessage("Okta", 0), /no password/);
  assert.match(deleteProviderMessage("Okta", 1), /1 user on these domains has no password/);
  assert.match(deleteProviderMessage("Okta", 3), /3 users on these domains have no password/);
});

test("ssoButtonName", () => {
  assert.equal(ssoButtonName({ name: " Demo IdP " }), "Demo IdP");
  assert.equal(ssoButtonName({}), "");
  assert.equal(ssoButtonName(null), "");
  assert.equal(ssoButtonName({ name: 3 }), "");
});

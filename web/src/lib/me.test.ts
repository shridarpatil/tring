import test from "node:test";
import assert from "node:assert/strict";
import { fmtTalk, memberTone } from "./me.ts";

test("fmtTalk", () => {
  assert.equal(fmtTalk(0), "0s");
  assert.equal(fmtTalk(45), "45s");
  assert.equal(fmtTalk(65), "1m 05s");
  assert.equal(fmtTalk(3720), "1h 02m");
  assert.equal(fmtTalk(-5), "0s");
});

test("memberTone", () => {
  assert.equal(memberTone("online"), "success");
  assert.equal(memberTone("on_call"), "live");
  assert.equal(memberTone("away"), "warning");
  assert.equal(memberTone("unknown"), "neutral");
});

import test from "node:test";
import assert from "node:assert/strict";
import { fmtTalk, longestWaitSeconds, memberTone, queueSummary } from "./me.ts";

test("fmtTalk", () => {
  assert.equal(fmtTalk(0), "0s");
  assert.equal(fmtTalk(45), "45s");
  assert.equal(fmtTalk(65), "1m 05s");
  assert.equal(fmtTalk(3720), "1h 02m");
  assert.equal(fmtTalk(-5), "0s");
});

test("longestWaitSeconds uses the oldest stint", () => {
  const now = Date.parse("2026-01-01T00:02:00Z");
  assert.equal(longestWaitSeconds(["2026-01-01T00:00:55Z", "2026-01-01T00:01:30Z"], now), 65);
  assert.equal(longestWaitSeconds([], now), 0);
  assert.equal(longestWaitSeconds(null, now), 0);
});

test("queueSummary", () => {
  assert.equal(queueSummary(0, 0), "Queue empty");
  assert.equal(queueSummary(3, 65), "3 waiting, longest 1m 05s");
});

test("memberTone", () => {
  assert.equal(memberTone("online"), "success");
  assert.equal(memberTone("on_call"), "live");
  assert.equal(memberTone("away"), "warning");
  assert.equal(memberTone("unknown"), "neutral");
});

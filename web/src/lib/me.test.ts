import test from "node:test";
import assert from "node:assert/strict";
import { fmtTalk, memberTone, pollWantsStats } from "./me.ts";

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

test("pollWantsStats: first poll and every 6th after", () => {
  assert.deepEqual(
    [0, 1, 5, 6, 7, 12].map((n) => pollWantsStats(n)),
    [true, false, false, true, false, true],
  );
  assert.equal(pollWantsStats(3, 3), true);
});

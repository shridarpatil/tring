import test from "node:test";
import assert from "node:assert/strict";
import { agentSignalState } from "./status.ts";

test("agentSignalState maps wrap_up to its own state", () => {
  assert.equal(agentSignalState("wrap_up"), "wrap_up");
  assert.equal(agentSignalState("bogus"), "offline");
});

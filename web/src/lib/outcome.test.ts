import test from "node:test";
import assert from "node:assert/strict";
import { outcomePatch } from "./outcome.ts";

const base = { disposition: { id: "d1" }, notes: "hi", tags: [{ id: "a" }, { id: "b" }] };
const same = { dispositionId: "d1", notes: "hi", tagIds: ["a", "b"] };

test("nothing changed sends nothing", () => {
  assert.deepEqual(outcomePatch(base, same), {});
});
test("only notes changed", () => {
  assert.deepEqual(outcomePatch(base, { ...same, notes: "bye" }), { notes: "bye" });
});
test("reordered tags are not a change", () => {
  assert.deepEqual(outcomePatch(base, { ...same, tagIds: ["b", "a"] }), {});
});
test("added tag sends the full set", () => {
  assert.deepEqual(outcomePatch(base, { ...same, tagIds: ["a", "b", "c"] }), { tag_ids: ["a", "b", "c"] });
});
test("cleared disposition is explicit null", () => {
  assert.deepEqual(outcomePatch(base, { ...same, dispositionId: "" }), { disposition_id: null });
});
test("emptied notes clear, untouched whitespace notes are not sent", () => {
  assert.deepEqual(outcomePatch(base, { ...same, notes: "" }), { notes: null });
  assert.deepEqual(outcomePatch({ notes: "  " }, { dispositionId: "", notes: "  ", tagIds: [] }), {});
});

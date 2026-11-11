import test from "node:test";
import assert from "node:assert/strict";
import { recipientSummary, defaultCampaignConfig, campaignTone } from "./campaigns.ts";

test("recipient progress counts all terminal outcomes, never active calls as delivered", () => {
  assert.deepEqual(recipientSummary({ pending: 5, active: 2, completed: 3, no_answer: 2, unknown: 1, cancelled: 4 }), { total: 17, pending: 5, active: 2, finished: 10, completed: 3 });
  assert.deepEqual(recipientSummary(null), { total: 0, pending: 0, active: 0, finished: 0, completed: 0 });
});
test("new campaign settings are independent and default to one attempt", () => {
  const a = defaultCampaignConfig(); a.max_attempts = 4;
  assert.equal(defaultCampaignConfig().max_attempts, 1);
});
test("status tones separate live, good, bad and attention states", () => {
  assert.equal(campaignTone("running"), "live");
  assert.equal(campaignTone("active"), "live");
  assert.equal(campaignTone("completed"), "success");
  assert.equal(campaignTone("failed"), "danger");
  assert.equal(campaignTone("unknown"), "warning");
  assert.equal(campaignTone("scheduled"), "accent");
  assert.equal(campaignTone("draft"), "neutral");
  assert.equal(campaignTone("something-new"), "neutral");
});

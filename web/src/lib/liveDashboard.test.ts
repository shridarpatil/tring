import test from "node:test";
import assert from "node:assert/strict";
import {
  buildAlerts,
  fmtMMSS,
  parseMMSS,
  sortTeams,
  teamState,
  waitTone,
} from "./liveDashboard.ts";
import type { QueueDepth } from "../api/types";

const NOW = Date.parse("2026-11-01T10:00:00Z");
const ago = (secs: number) => new Date(NOW - secs * 1000).toISOString();

function team(over: Partial<QueueDepth>): QueueDepth {
  return {
    team: "support",
    waiting: 0,
    open: true,
    min_live_agents: 0,
    sla_seconds: 0,
    members: 0,
    live: 0,
    on_call: 0,
    available: 0,
    away: 0,
    waiting_since: [],
    breaches_today: 0,
    hours: [],
    ...over,
  };
}

test("understaffed open team is crit and alerts", () => {
  const t = team({ min_live_agents: 10, live: 5 });
  assert.equal(teamState(t, NOW), "crit");
  const a = buildAlerts([t], NOW);
  assert.equal(a.length, 1);
  assert.equal(a[0].kind, "understaffed");
  assert.equal(a[0].live, 5);
  assert.equal(a[0].min, 10);
});

test("closed team below minimum does not alert and is closed", () => {
  const t = team({ open: false, min_live_agents: 10, live: 0 });
  assert.equal(teamState(t, NOW), "closed");
  assert.deepEqual(buildAlerts([t], NOW), []);
});

test("closed team with a caller past SLA is crit and gets an sla alert", () => {
  const t = team({ open: false, sla_seconds: 60, waiting: 1, waiting_since: [ago(90)] });
  assert.equal(teamState(t, NOW), "crit");
  const a = buildAlerts([t], NOW);
  assert.equal(a.length, 1);
  assert.equal(a[0].kind, "sla");
  assert.equal(a[0].breaching, 1);
});

test("unconfigured team never alerts", () => {
  const t = team({ waiting: 3, waiting_since: [ago(3600), ago(3600), ago(3600)] });
  assert.equal(teamState(t, NOW), "ok");
  assert.deepEqual(buildAlerts([t], NOW), []);
});

test("near SLA is warn at 80%", () => {
  assert.equal(teamState(team({ sla_seconds: 300, waiting: 1, waiting_since: [ago(241)] }), NOW), "warn");
  assert.equal(teamState(team({ sla_seconds: 300, waiting: 1, waiting_since: [ago(239)] }), NOW), "ok");
});

test("alerts order: understaffed by deficit, then sla by breaching", () => {
  const small = team({ team: "small", min_live_agents: 4, live: 3 });
  const big = team({ team: "big", min_live_agents: 10, live: 2 });
  const sla1 = team({ team: "sla1", sla_seconds: 60, waiting_since: [ago(100)] });
  const sla2 = team({ team: "sla2", sla_seconds: 60, waiting_since: [ago(100), ago(70)] });
  const got = buildAlerts([sla1, small, sla2, big], NOW).map((a) => a.team);
  assert.deepEqual(got, ["big", "small", "sla2", "sla1"]);
});

test("sortTeams puts closed last", () => {
  const closed = team({ team: "a-closed", open: false });
  const ok = team({ team: "z-ok" });
  const crit = team({ team: "m-crit", min_live_agents: 2, live: 0 });
  assert.deepEqual(
    sortTeams([closed, ok, crit], NOW).map((t) => t.team),
    ["m-crit", "z-ok", "a-closed"],
  );
});

test("waitTone", () => {
  assert.equal(waitTone(100, 300), "");
  assert.equal(waitTone(250, 300), "warning");
  assert.equal(waitTone(301, 300), "danger");
  assert.equal(waitTone(9999, 0), "");
});

test("parseMMSS / fmtMMSS round trip", () => {
  assert.equal(parseMMSS("5:00"), 300);
  assert.equal(parseMMSS("300"), 300);
  assert.equal(parseMMSS("1:5"), null);
  assert.equal(parseMMSS(""), null);
  assert.equal(fmtMMSS(300), "5:00");
  assert.equal(fmtMMSS(65), "1:05");
  assert.equal(parseMMSS(fmtMMSS(754)), 754);
});

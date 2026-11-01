import test from "node:test";
import assert from "node:assert/strict";
import {
  agentTeams,
  belowMinCount,
  staffedPct,
  staffingSegments,
  teamView,
  agentsLiveRequired,
  alertCopy,
  alertKey,
  visibleAlerts,
  SNOOZE_MS,
  buildAlerts,
  callSlaBadge,
  liveCallBadge,
  liveCallSecs,
  filterLiveCalls,
  todayHours,
  isUnderstaffed,
  isWaitingCall,
  sortLiveCalls,
  teamBadge,
  fmtMMSS,
  parseMMSS,
  sortTeams,
  teamState,
  waitTone,
  sharedSla,
  abandonedPct,
} from "./liveDashboard.ts";
import type { Call, QueueDepth } from "../api/types";

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
    agents: [],
    staffing_known: true,
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

test("todayHours shows only today's timing", () => {
  const wk = (d: string, enabled: boolean, s?: string, e?: string) => ({ day: d, enabled, start_time: s, end_time: e });
  const mon = new Date(2026, 10, 2, 12); // Monday
  const sun = new Date(2026, 10, 1, 12); // Sunday
  assert.equal(todayHours([], mon), "Always open");
  assert.equal(todayHours([wk("tuesday", true, "09:00", "17:00")], mon), "Closed today");
  assert.equal(todayHours([wk("monday", false, "09:00", "17:00")], mon), "Closed today");
  assert.equal(todayHours([wk("Monday", true, "10:30", "18:00")], mon), "Today 10:30–18:00");
  assert.equal(todayHours([wk("monday", true, "09:00", "17:00")], mon), "Today 9:00–17:00");
  assert.equal(todayHours([wk("sunday", true, "00:00", "23:59")], sun), "Today 0:00–23:59");
  assert.equal(todayHours([wk("monday", true)], mon), "Open all day");
  assert.equal(todayHours([wk("monday", true, "9am", "")], mon), "Open all day");
});

test("teamBadge", () => {
  assert.deepEqual(teamBadge(team({ min_live_agents: 10, live: 5 }), NOW), { text: "Understaffed −5", variant: "danger" });
  assert.deepEqual(teamBadge(team({ sla_seconds: 60, waiting_since: [ago(90)] }), NOW), { text: "SLA breach", variant: "danger" });
  assert.deepEqual(teamBadge(team({ sla_seconds: 100, waiting_since: [ago(90)] }), NOW), { text: "At risk", variant: "warning" });
  assert.deepEqual(teamBadge(team({}), NOW), { text: "OK", variant: "success" });
  assert.deepEqual(teamBadge(team({ open: false }), NOW), { text: "Closed", variant: "secondary" });
});

test("alertCopy understaffed", () => {
  const [a] = buildAlerts(
    [team({ team: "Support", min_live_agents: 10, live: 5, away: 2, sla_seconds: 300, waiting_since: [ago(400), ago(30)], below_min_since: ago(725) })],
    NOW,
  );
  assert.deepEqual(alertCopy(a), {
    strong: "Support is understaffed",
    rest: ": 5 of 10 agents live (−5) · 2 waiting, 1 past 5:00 SLA, longest 6:40 · 2 on Away",
    sub: "below minimum for 12 min",
  });
  const [b] = buildAlerts([team({ team: "Sales", min_live_agents: 2, live: 1 })], NOW);
  assert.deepEqual(alertCopy(b), {
    strong: "Sales is understaffed",
    rest: ": 1 of 2 agents live (−1) · 0 waiting, longest 0:00",
    sub: null,
  });
});

test("alertCopy sla", () => {
  const [a] = buildAlerts([team({ team: "Sales", min_live_agents: 2, live: 2, sla_seconds: 120, waiting_since: [ago(130)] })], NOW);
  assert.deepEqual(alertCopy(a), {
    strong: "Sales: 1 caller past the 2:00 SLA",
    rest: ", longest waiting 2:10 · 2 of 2 agents live",
    sub: null,
  });
  const [b] = buildAlerts([team({ team: "KYC", sla_seconds: 60, waiting_since: [ago(200), ago(90)] })], NOW);
  assert.equal(alertCopy(b).strong, "KYC: 2 callers past the 1:00 SLA");
  assert.equal(alertCopy(b).rest, ", longest waiting 3:20");
});

function call(over: Partial<Call>): Call {
  return { id: "c", direction: "inbound", status: "ringing", from_number: "", to_number: "", leg_id: "", started_at: ago(10), ...over };
}

test("sortLiveCalls puts waiting callers first, longest first", () => {
  const talk = call({ id: "talk", status: "answered" });
  const ivr = call({ id: "ivr" });
  const w1 = call({ id: "w1", queued_at: ago(30) });
  const w2 = call({ id: "w2", queued_at: ago(300) });
  assert.deepEqual(sortLiveCalls([talk, ivr, w1, w2]).map((c) => c.id), ["w2", "w1", "talk", "ivr"]);
});

test("callSlaBadge", () => {
  assert.equal(callSlaBadge(call({ queued_at: ago(100), queue_sla_seconds: 300 }), NOW), null);
  assert.deepEqual(callSlaBadge(call({ queued_at: ago(250), queue_sla_seconds: 300 }), NOW), { text: "0:50 to SLA", variant: "warning" });
  assert.deepEqual(callSlaBadge(call({ queued_at: ago(301), queue_sla_seconds: 300 }), NOW), { text: "SLA breached", variant: "danger" });
  assert.equal(callSlaBadge(call({ queued_at: ago(999) }), NOW), null);
  assert.equal(callSlaBadge(call({ status: "answered", queue_sla_seconds: 60 }), NOW), null);
});

test("liveCallBadge", () => {
  assert.deepEqual(liveCallBadge(call({ queued_at: ago(5), phase: "ringing" })), { text: "waiting", variant: "danger" });
  assert.deepEqual(liveCallBadge(call({ status: "answered", phase: "ringing" })), { text: "ringing", variant: "accent" });
  assert.deepEqual(liveCallBadge(call({ status: "answered" })), { text: "talking", variant: "live" });
  assert.deepEqual(liveCallBadge(call({ phase: "ivr" })), { text: "in IVR", variant: "secondary" });
  assert.deepEqual(liveCallBadge(call({})), { text: "ringing", variant: "accent" });
});

test("liveCallSecs", () => {
  assert.equal(liveCallSecs(call({ started_at: ago(400), queued_at: ago(90) }), NOW), 90);
  assert.equal(liveCallSecs(call({ status: "answered", started_at: ago(400), answered_at: ago(60) }), NOW), 60);
  assert.equal(liveCallSecs(call({ started_at: ago(400) }), NOW), 400);
});

test("agentsLiveRequired counts seats covered per team", () => {
  assert.deepEqual(
    agentsLiveRequired([
      team({ live: 80, min_live_agents: 50 }),
      team({ live: 20, min_live_agents: 50 }),
    ]),
    { live: 70, min: 100, short: 1 },
  );
});

test("agentsLiveRequired ignores min-0, closed and unknown-staffing teams", () => {
  assert.deepEqual(
    agentsLiveRequired([
      team({ live: 5, min_live_agents: 0 }),
      team({ open: false, live: 1, min_live_agents: 4 }),
      team({ staffing_known: false, live: 0, min_live_agents: 6 }),
      team({ live: 2, min_live_agents: 2 }),
    ]),
    { live: 2, min: 2, short: 0 },
  );
});

test("filterLiveCalls keeps the supervised call whatever the filter", () => {
  const talk = call({ id: "talk", status: "answered" });
  const w = call({ id: "w", queued_at: ago(30) });
  const ivr = call({ id: "ivr" });
  assert.deepEqual(filterLiveCalls([w, talk, ivr], "waiting").map((c) => c.id), ["w"]);
  assert.deepEqual(filterLiveCalls([w, talk, ivr], "waiting", "talk").map((c) => c.id), ["w", "talk"]);
  assert.deepEqual(filterLiveCalls([w, talk, ivr], "talking", "ivr").map((c) => c.id), ["talk", "ivr"]);
  assert.deepEqual(filterLiveCalls([w, talk, ivr], "all").map((c) => c.id), ["w", "talk", "ivr"]);
});

test("staffedPct", () => {
  assert.equal(staffedPct(team({ min_live_agents: 10, live: 5 })), 50);
  assert.equal(staffedPct(team({ min_live_agents: 3, live: 2 })), 67);
  assert.equal(staffedPct(team({ min_live_agents: 0, live: 2 })), null);
});

test("agentTeams maps sip to sorted team names", () => {
  const m = agentTeams([
    team({ team: "Sales", agents: ["amy", "bob"] }),
    team({ team: "Billing", agents: ["amy"] }),
  ]);
  assert.deepEqual(m.get("amy"), ["Billing", "Sales"]);
  assert.deepEqual(m.get("bob"), ["Sales"]);
  assert.equal(m.get("zed"), undefined);
});

test("belowMinCount counts open understaffed teams", () => {
  assert.equal(
    belowMinCount([
      team({ min_live_agents: 2, live: 1 }),
      team({ min_live_agents: 2, live: 2 }),
      team({ open: false, min_live_agents: 2, live: 0 }),
    ]),
    1,
  );
});

test("teamView: auto switches at CARDS_MAX_TEAMS, others force", () => {
  assert.equal(teamView("auto", 6), "cards");
  assert.equal(teamView("auto", 7), "rows");
  assert.equal(teamView("cards", 10), "cards");
  assert.equal(teamView("rows", 1), "rows");
});

test("a call blind-transferred into a queue is waiting even though it was answered", () => {
  const c = call({ status: "answered", queued_at: ago(400), queue_team: "Support", queue_sla_seconds: 300 });
  assert.equal(isWaitingCall(c), true);
  assert.deepEqual(callSlaBadge(c, NOW), { text: "SLA breached", variant: "danger" });
  assert.deepEqual(sortLiveCalls([call({ id: "x" }), { ...c, id: "t" }]).map((x) => x.id), ["t", "x"]);
});

test("unknown staffing is never understaffed", () => {
  const t = team({ min_live_agents: 10, live: 0, staffing_known: false });
  assert.equal(isUnderstaffed(t), false);
  assert.deepEqual(buildAlerts([t], NOW), []);
  assert.equal(belowMinCount([t]), 0);
});

test("sharedSla: one common limit, else null", () => {
  const t = (sla: number) => ({ sla_seconds: sla }) as QueueDepth;
  assert.equal(sharedSla([t(300), t(300), t(0)]), 300);
  assert.equal(sharedSla([t(300), t(120)]), null);
  assert.equal(sharedSla([t(0)]), null);
  assert.equal(sharedSla([]), null);
});

test("abandonedPct: one decimal, null without calls", () => {
  assert.equal(abandonedPct(23, 406), "5.7");
  assert.equal(abandonedPct(0, 10), "0.0");
  assert.equal(abandonedPct(3, 0), null);
});

test("staffingSegments: Support-like team with a gap and away agents", () => {
  const segs = staffingSegments({ min_live_agents: 10, live: 5, on_call: 4, available: 1, away: 2 } as QueueDepth);
  assert.deepEqual(
    segs.map((g) => [g.kind, g.count]),
    [["on_call", 4], ["available", 1], ["missing", 5], ["away", 2]],
  );
  assert.ok(Math.abs(segs[0].pct - (4 / 12) * 100) < 1e-9);
  assert.ok(Math.abs(segs[2].pct - (5 / 12) * 100) < 1e-9);
  assert.ok(Math.abs(segs.reduce((a, g) => a + g.pct, 0) - 100) < 1e-9);
});

test("staffingSegments: over-staffed has no missing segment", () => {
  const segs = staffingSegments({ min_live_agents: 2, live: 3, on_call: 1, available: 2, away: 0 } as QueueDepth);
  assert.deepEqual(segs.map((g) => g.kind), ["on_call", "available"]);
  assert.ok(Math.abs(segs[0].pct - 100 / 3) < 1e-9);
  assert.ok(Math.abs(segs.reduce((a, g) => a + g.pct, 0) - 100) < 1e-9);
});

test("staffingSegments: no minimum gives no segments", () => {
  assert.deepEqual(staffingSegments({ min_live_agents: 0, live: 3, on_call: 1, available: 2, away: 1 } as QueueDepth), []);
});

test("visibleAlerts: snoozed alert hidden until expiry, then shown", () => {
  const a = { team: "sales", kind: "sla" } as DashAlert;
  const snoozed = { [alertKey(a)]: 1000 + SNOOZE_MS };
  assert.deepEqual(visibleAlerts([a], snoozed, 1000), []);
  assert.deepEqual(visibleAlerts([a], snoozed, 1000 + SNOOZE_MS - 1), []);
  assert.deepEqual(visibleAlerts([a], snoozed, 1000 + SNOOZE_MS), [a]);
});

test("visibleAlerts: other teams and kinds are unaffected", () => {
  const sla = { team: "sales", kind: "sla" } as DashAlert;
  const under = { team: "sales", kind: "understaffed" } as DashAlert;
  const other = { team: "support", kind: "sla" } as DashAlert;
  const snoozed = { [alertKey(sla)]: 5000 };
  assert.deepEqual(visibleAlerts([sla, under, other], snoozed, 1000), [under, other]);
});

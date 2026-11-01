import test from "node:test";
import assert from "node:assert/strict";
import {
  agentTeams,
  belowMinCount,
  seatStates,
  showSeats,
  staffedPct,
  teamView,
  agentsLiveRequired,
  alertCopy,
  buildAlerts,
  callSlaBadge,
  filterLiveCalls,
  hoursSummary,
  isUnderstaffed,
  isWaitingCall,
  sortLiveCalls,
  teamBadge,
  fmtMMSS,
  parseMMSS,
  sortTeams,
  teamState,
  waitTone,
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

test("hoursSummary groups consecutive days with the same hours", () => {
  const wk = (d: string, enabled: boolean, s?: string, e?: string) => ({ day: d, enabled, start_time: s, end_time: e });
  assert.equal(hoursSummary([]), "Always open");
  assert.equal(
    hoursSummary([
      wk("monday", true, "09:00", "17:00"),
      wk("tuesday", true, "09:00", "17:00"),
      wk("wednesday", true, "09:00", "17:00"),
      wk("thursday", true, "09:00", "17:00"),
      wk("friday", true, "09:00", "17:00"),
      wk("saturday", false),
      wk("sunday", true, "00:00", "23:59"),
    ]),
    "Mon–Fri 9:00–17:00 · Sun 0:00–23:59",
  );
  assert.equal(hoursSummary([wk("monday", true), wk("tuesday", true, "10:30", "18:00")]), "Mon all day · Tue 10:30–18:00");
  assert.equal(hoursSummary([wk("monday", false)]), "Closed all week");
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

test("agentsLiveRequired sums open teams only", () => {
  assert.deepEqual(
    agentsLiveRequired([
      team({ live: 5, min_live_agents: 10 }),
      team({ live: 2, min_live_agents: 2 }),
      team({ open: false, live: 1, min_live_agents: 4 }),
      team({ staffing_known: false, live: 0, min_live_agents: 6 }),
    ]),
    { live: 7, min: 12 },
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

test("seatStates: on call, available, empty seats, away", () => {
  const t = team({ min_live_agents: 6, live: 3, on_call: 2, available: 1, away: 2 });
  assert.deepEqual(seatStates(t), ["on_call", "on_call", "online", "offline", "offline", "offline", "away", "away"]);
  assert.deepEqual(seatStates(team({ min_live_agents: 1, live: 2, on_call: 1, available: 1 })), ["on_call", "online"]);
});

test("showSeats only for small teams", () => {
  assert.equal(showSeats(team({ min_live_agents: 10, members: 12 })), true);
  assert.equal(showSeats(team({ min_live_agents: 25, members: 12 })), false);
  assert.equal(showSeats(team({ min_live_agents: 2, members: 120 })), false);
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

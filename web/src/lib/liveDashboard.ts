import type { Call, QueueDepth, TeamDay } from "../api/types";

export const NEAR_SLA = 0.8; // warn once the longest wait passes this share of the SLA
export const CARDS_MAX_TEAMS = 6;

export type TeamState = "crit" | "warn" | "ok" | "closed";
export type Tone = "" | "warning" | "danger";

export interface DashAlert {
  team: string;
  kind: "understaffed" | "sla";
  live: number;
  min: number;
  waiting: number;
  breaching: number;
  longestSecs: number;
  away: number;
  belowSinceSecs: number | null;
  slaSeconds: number;
}

export function waitSecs(sinceIso: string, nowMs: number): number {
  return Math.max(0, Math.floor((nowMs - Date.parse(sinceIso)) / 1000));
}

export function longestWait(t: QueueDepth, nowMs: number): number {
  // waiting_since is oldest first.
  return t.waiting_since.length ? waitSecs(t.waiting_since[0], nowMs) : 0;
}

export function breachingCount(t: QueueDepth, nowMs: number): number {
  if (t.sla_seconds <= 0) return 0;
  return t.waiting_since.filter((s) => waitSecs(s, nowMs) > t.sla_seconds).length;
}

// Unknown staffing (presence unreadable) never counts as understaffed.
export function isUnderstaffed(t: QueueDepth): boolean {
  return t.staffing_known && t.open && t.min_live_agents > 0 && t.live < t.min_live_agents;
}

export function teamState(t: QueueDepth, nowMs: number): TeamState {
  if (isUnderstaffed(t) || breachingCount(t, nowMs) > 0) return "crit";
  if (t.sla_seconds > 0 && longestWait(t, nowMs) > NEAR_SLA * t.sla_seconds) return "warn";
  if (!t.open) return "closed";
  return "ok";
}

const deficit = (t: QueueDepth) => (isUnderstaffed(t) ? t.min_live_agents - t.live : 0);
const STATE_RANK: Record<TeamState, number> = { crit: 0, warn: 1, ok: 2, closed: 3 };

export function sortTeams(ts: QueueDepth[], nowMs: number): QueueDepth[] {
  return [...ts].sort(
    (a, b) =>
      STATE_RANK[teamState(a, nowMs)] - STATE_RANK[teamState(b, nowMs)] ||
      deficit(b) - deficit(a) ||
      longestWait(b, nowMs) - longestWait(a, nowMs) ||
      a.team.localeCompare(b.team),
  );
}

export const SNOOZE_MS = 15 * 60 * 1000;

export function alertKey(a: DashAlert): string {
  return `${a.team}|${a.kind}`;
}

// Drops alerts the supervisor snoozed; a snooze lapses once nowMs reaches its until.
export function visibleAlerts(alerts: DashAlert[], snoozed: Record<string, number>, nowMs: number): DashAlert[] {
  return alerts.filter((a) => !((snoozed[alertKey(a)] ?? 0) > nowMs));
}

// Understaffed teams first (biggest deficit first; their breaching callers ride
// along), then teams that only have callers past their SLA.
export function buildAlerts(ts: QueueDepth[], nowMs: number): DashAlert[] {
  const make = (t: QueueDepth, kind: DashAlert["kind"]): DashAlert => ({
    team: t.team,
    kind,
    live: t.live,
    min: t.min_live_agents,
    waiting: t.waiting_since.length,
    breaching: breachingCount(t, nowMs),
    longestSecs: longestWait(t, nowMs),
    away: t.away,
    belowSinceSecs: t.below_min_since ? waitSecs(t.below_min_since, nowMs) : null,
    slaSeconds: t.sla_seconds,
  });
  const under = ts.filter(isUnderstaffed).sort((a, b) => deficit(b) - deficit(a) || a.team.localeCompare(b.team));
  const sla = ts
    .filter((t) => !isUnderstaffed(t) && breachingCount(t, nowMs) > 0)
    .sort((a, b) => breachingCount(b, nowMs) - breachingCount(a, nowMs) || a.team.localeCompare(b.team));
  return [...under.map((t) => make(t, "understaffed")), ...sla.map((t) => make(t, "sla"))];
}

export function waitTone(secs: number, slaSeconds: number): Tone {
  if (slaSeconds <= 0) return "";
  if (secs > slaSeconds) return "danger";
  if (secs > NEAR_SLA * slaSeconds) return "warning";
  return "";
}

export function fmtMMSS(secs: number): string {
  const s = Math.max(0, Math.round(secs));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

// "5:00" -> 300, "300" -> 300; anything else (including "1:5") -> null.
export function parseMMSS(s: string): number | null {
  const v = s.trim();
  let m = /^(\d+):([0-5]\d)$/.exec(v);
  if (m) return Number(m[1]) * 60 + Number(m[2]);
  m = /^(\d+)$/.exec(v);
  return m ? Number(m[1]) : null;
}

export type BadgeVariant = "danger" | "warning" | "success" | "secondary";

export function teamBadge(t: QueueDepth, nowMs: number): { text: string; variant: BadgeVariant } {
  switch (teamState(t, nowMs)) {
    case "crit":
      return isUnderstaffed(t)
        ? { text: `Understaffed −${deficit(t)}`, variant: "danger" }
        : { text: "SLA breach", variant: "danger" };
    case "warn":
      return { text: "At risk", variant: "warning" };
    case "closed":
      return { text: "Closed", variant: "secondary" };
    default:
      return { text: "OK", variant: "success" };
  }
}

// Alert text: a bold lead, the rest of the line, and an optional second line.
// "for how long" wording: "under a minute", "14 min", "1h 20m", "8h".
export function fmtSince(secs: number): string {
  const m = Math.floor(Math.max(0, secs) / 60);
  if (m < 1) return "under a minute";
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60), rem = m % 60;
  return rem ? `${h}h ${rem}m` : `${h}h`;
}

export function alertCopy(a: DashAlert): { strong: string; rest: string; sub: string | null } {
  if (a.kind === "understaffed") {
    let rest = `: ${a.live} of ${a.min} agents live (−${a.min - a.live}) · ${a.waiting} waiting`;
    if (a.breaching > 0) rest += `, ${a.breaching} past ${fmtMMSS(a.slaSeconds)} SLA`;
    if (a.waiting > 0) rest += `, longest ${fmtMMSS(a.longestSecs)}`;
    if (a.away > 0) rest += ` · ${a.away} on Away`;
    const sub = a.belowSinceSecs !== null ? `below minimum for ${fmtSince(a.belowSinceSecs)}` : null;
    return { strong: `${a.team} is understaffed`, rest, sub };
  }
  const callers = a.breaching === 1 ? "caller" : "callers";
  let rest = `, longest waiting ${fmtMMSS(a.longestSecs)}`;
  if (a.min > 0) rest += ` · ${a.live} of ${a.min} agents live`;
  return { strong: `${a.team}: ${a.breaching} ${callers} past the ${fmtMMSS(a.slaSeconds)} SLA`, rest, sub: null };
}

const DAY_NAMES = ["sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"];
const HHMM = /^\d{1,2}:\d{2}$/;
const hm = (v: string) => v.replace(/^0(\d)/, "$1"); // "09:00" -> "9:00"

// Today's timing only, mirroring the server's timing evaluation: no schedule =
// always open; today missing or disabled = closed; enabled without valid
// times = open all day.
export function todayHours(hours: TeamDay[], now: Date): string {
  if (!hours.length) return "Always open";
  const name = DAY_NAMES[now.getDay()];
  const d = hours.find((h) => h.day.toLowerCase() === name);
  if (!d || !d.enabled) return "Closed today";
  if (d.start_time && d.end_time && HHMM.test(d.start_time) && HHMM.test(d.end_time)) {
    return `Today ${hm(d.start_time)}–${hm(d.end_time)}`;
  }
  return "Open all day";
}

// queued_at is set while the caller has an open queue stint (the leave event
// clears it). It can be an answered call: a blind transfer back into a queue.
export function isWaitingCall(c: Call): boolean {
  return !!c.queued_at;
}

// Waiting callers first, longest wait first; everything else keeps its order.
export function sortLiveCalls(cs: Call[]): Call[] {
  const waiting = cs.filter(isWaitingCall).sort((a, b) => Date.parse(a.queued_at!) - Date.parse(b.queued_at!));
  return [...waiting, ...cs.filter((c) => !isWaitingCall(c))];
}

export type CallFilter = "all" | "waiting" | "talking";

// Apply the live-calls filter, always keeping keepId (the call being
// supervised) so its Whisper/Barge/Stop controls stay reachable.
export function filterLiveCalls(cs: Call[], f: CallFilter, keepId?: string): Call[] {
  return cs.filter(
    (c) => f === "all" || c.id === keepId || (f === "waiting" ? isWaitingCall(c) : c.status === "answered"),
  );
}

// Live-call status badge. A queued caller reads "waiting" (even while the queue
// rings an agent). Otherwise: an active ring first, so a transfer re-ring on an
// answered call reads "ringing" until the new agent picks up; then talking
// (answered); then in IVR (caller in the menu, nobody being rung yet).
export function liveCallBadge(c: Call): { text: string; variant: string } {
  if (isWaitingCall(c)) return { text: "waiting", variant: "danger" };
  if (c.phase === "ringing") return { text: "ringing", variant: "accent" };
  if (c.status === "answered") return { text: "talking", variant: "live" };
  if (c.phase === "ivr") return { text: "in IVR", variant: "secondary" };
  return { text: "ringing", variant: "accent" };
}

// The Time column: wait so far for a queued caller, talk time once answered,
// otherwise time since the call started.
export function liveCallSecs(c: Call, nowMs: number): number {
  if (isWaitingCall(c)) return waitSecs(c.queued_at!, nowMs);
  return Math.max(0, (nowMs - Date.parse(c.answered_at ?? c.started_at)) / 1000);
}

export function callSlaBadge(c: Call, nowMs: number): { text: string; variant: "danger" | "warning" } | null {
  const sla = c.queue_sla_seconds ?? 0;
  if (!isWaitingCall(c) || sla <= 0) return null;
  const secs = waitSecs(c.queued_at!, nowMs);
  const tone = waitTone(secs, sla);
  if (tone === "danger") return { text: "SLA breached", variant: "danger" };
  if (tone === "warning") return { text: `${fmtMMSS(sla - secs)} to SLA`, variant: "warning" };
  return null;
}

// The SLA limit when every team with one configured shares it; null otherwise.
export function sharedSla(ts: QueueDepth[]): number | null {
  const set = new Set(ts.filter((t) => t.sla_seconds > 0).map((t) => t.sla_seconds));
  return set.size === 1 ? [...set][0] : null;
}

// Abandoned share of today's calls, one decimal ("5.6"); null when there were no calls.
export function abandonedPct(abandoned: number, calls: number): string | null {
  return calls > 0 ? ((abandoned / calls) * 100).toFixed(1) : null;
}

// Live agents as a share of the minimum; null when the team has no minimum.
export function staffedPct(t: QueueDepth): number | null {
  return t.min_live_agents > 0 ? Math.round((t.live / t.min_live_agents) * 100) : null;
}

export interface StaffSegment {
  kind: "on_call" | "available" | "missing" | "away";
  count: number;
  pct: number;
}

// Segments of the staffing bar, left to right; zero-count kinds omitted.
// Total = max(min, live) + away. Empty when the team has no minimum.
export function staffingSegments(t: QueueDepth): StaffSegment[] {
  if (t.min_live_agents <= 0) return [];
  const total = Math.max(t.min_live_agents, t.live) + t.away;
  const parts: [StaffSegment["kind"], number][] = [
    ["on_call", t.on_call],
    ["available", t.available],
    ["missing", Math.max(0, t.min_live_agents - t.live)],
    ["away", t.away],
  ];
  return parts.filter(([, n]) => n > 0).map(([kind, count]) => ({ kind, count, pct: (count / total) * 100 }));
}

// SIP username -> the teams that agent belongs to, sorted.
export function agentTeams(ts: QueueDepth[]): Map<string, string[]> {
  const m = new Map<string, string[]>();
  for (const t of ts) for (const sip of t.agents) m.set(sip, [...(m.get(sip) ?? []), t.team]);
  for (const l of m.values()) l.sort((a, b) => a.localeCompare(b));
  return m;
}

export function belowMinCount(ts: QueueDepth[]): number {
  return ts.filter(isUnderstaffed).length;
}

export type TeamViewMode = "auto" | "cards" | "rows";

export function teamView(mode: TeamViewMode, teamCount: number): "cards" | "rows" {
  if (mode !== "auto") return mode;
  return teamCount <= CARDS_MAX_TEAMS ? "cards" : "rows";
}

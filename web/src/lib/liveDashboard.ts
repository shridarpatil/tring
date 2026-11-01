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

export function isUnderstaffed(t: QueueDepth): boolean {
  return t.open && t.min_live_agents > 0 && t.live < t.min_live_agents;
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
export function alertCopy(a: DashAlert): { strong: string; rest: string; sub: string | null } {
  if (a.kind === "understaffed") {
    let rest = `: ${a.live} of ${a.min} agents live (−${a.min - a.live}) · ${a.waiting} waiting`;
    if (a.breaching > 0) rest += `, ${a.breaching} past ${fmtMMSS(a.slaSeconds)} SLA`;
    rest += `, longest ${fmtMMSS(a.longestSecs)}`;
    if (a.away > 0) rest += ` · ${a.away} on Away`;
    const sub = a.belowSinceSecs !== null ? `below minimum for ${Math.floor(a.belowSinceSecs / 60)} min` : null;
    return { strong: `${a.team} is understaffed`, rest, sub };
  }
  const callers = a.breaching === 1 ? "caller" : "callers";
  let rest = `, longest waiting ${fmtMMSS(a.longestSecs)}`;
  if (a.min > 0) rest += ` · ${a.live} of ${a.min} agents live`;
  return { strong: `${a.team}: ${a.breaching} ${callers} past the ${fmtMMSS(a.slaSeconds)} SLA`, rest, sub: null };
}

const DAY_ABBR: Record<string, string> = {
  monday: "Mon", tuesday: "Tue", wednesday: "Wed", thursday: "Thu", friday: "Fri", saturday: "Sat", sunday: "Sun",
};
const hm = (v: string) => v.replace(/^0(\d)/, "$1"); // "09:00" -> "9:00"

// "Mon–Fri 9:00–17:00 · Sun 0:00–23:59"; consecutive days with the same hours
// collapse into a range. Empty schedule = always open.
export function hoursSummary(days: TeamDay[]): string {
  if (!days.length) return "Always open";
  const groups: { from: string; to: string; hours: string }[] = [];
  let prevIdx = -2;
  days.forEach((d, i) => {
    if (!d.enabled) return;
    const hours = d.start_time && d.end_time ? `${hm(d.start_time)}–${hm(d.end_time)}` : "all day";
    const name = DAY_ABBR[d.day.toLowerCase()] ?? d.day;
    const last = groups[groups.length - 1];
    if (last && prevIdx === i - 1 && last.hours === hours) last.to = name;
    else groups.push({ from: name, to: name, hours });
    prevIdx = i;
  });
  if (!groups.length) return "Closed all week";
  return groups.map((g) => `${g.from === g.to ? g.from : `${g.from}–${g.to}`} ${g.hours}`).join(" · ");
}

export function isWaitingCall(c: Call): boolean {
  return !!c.queued_at && c.status !== "answered";
}

// Waiting callers first, longest wait first; everything else keeps its order.
export function sortLiveCalls(cs: Call[]): Call[] {
  const waiting = cs.filter(isWaitingCall).sort((a, b) => Date.parse(a.queued_at!) - Date.parse(b.queued_at!));
  return [...waiting, ...cs.filter((c) => !isWaitingCall(c))];
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

// Agents live vs required, summed over teams that are open now.
export function agentsLiveRequired(ts: QueueDepth[]): { live: number; min: number } {
  const open = ts.filter((t) => t.open);
  return {
    live: open.reduce((n, t) => n + t.live, 0),
    min: open.reduce((n, t) => n + t.min_live_agents, 0),
  };
}

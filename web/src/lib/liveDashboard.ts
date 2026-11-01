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

export function callSlaBadge(c: Call, nowMs: number): { text: string; variant: "danger" | "warning" } | null {
  const sla = c.queue_sla_seconds ?? 0;
  if (!isWaitingCall(c) || sla <= 0) return null;
  const secs = waitSecs(c.queued_at!, nowMs);
  const tone = waitTone(secs, sla);
  if (tone === "danger") return { text: "SLA breached", variant: "danger" };
  if (tone === "warning") return { text: `${fmtMMSS(sla - secs)} to SLA`, variant: "warning" };
  return null;
}

// Agents live vs required, summed over teams that are open now and whose
// staffing is known.
export function agentsLiveRequired(ts: QueueDepth[]): { live: number; min: number } {
  const open = ts.filter((t) => t.open && t.staffing_known);
  return {
    live: open.reduce((n, t) => n + t.live, 0),
    min: open.reduce((n, t) => n + t.min_live_agents, 0),
  };
}

export type SeatState = "on_call" | "online" | "offline" | "away";
export const SEATS_MAX = 24; // above this a team shows a progress bar instead of seat dots

// One dot per seat: on call, available, empty seats up to the minimum, then Away.
export function seatStates(t: QueueDepth): SeatState[] {
  const n = (k: number, s: SeatState): SeatState[] => Array(Math.max(0, k)).fill(s);
  return [
    ...n(t.on_call, "on_call"),
    ...n(t.available, "online"),
    ...n(t.min_live_agents - t.live, "offline"),
    ...n(t.away, "away"),
  ];
}

export function showSeats(t: QueueDepth): boolean {
  return t.min_live_agents <= SEATS_MAX && t.members <= SEATS_MAX;
}

// Live agents as a share of the minimum; null when the team has no minimum.
export function staffedPct(t: QueueDepth): number | null {
  return t.min_live_agents > 0 ? Math.round((t.live / t.min_live_agents) * 100) : null;
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

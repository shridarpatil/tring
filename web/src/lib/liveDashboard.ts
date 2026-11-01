import type { QueueDepth } from "../api/types";

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

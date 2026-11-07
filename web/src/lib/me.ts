// Pure helpers for the agent's own page (/agent); kept free of Vue so they are
// unit-testable.

/** Talk time: "45s", "1m 05s", "1h 02m". */
export function fmtTalk(secs: number): string {
  const s = Math.max(0, Math.round(secs));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (s < 3600) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

/** Member presence status -> chip tone (matches the console's status language). */
export function memberTone(status: string): "success" | "live" | "warning" | "neutral" {
  switch (status) {
    case "online":
      return "success";
    case "on_call":
      return "live";
    case "away":
      return "warning";
    default:
      return "neutral";
  }
}

/**
 * The agent dashboard polls its live panels every 5 s but the day's stats only
 * every `every`th poll (30 s); poll 0, the first load, always includes them.
 */
export function pollWantsStats(n: number, every = 6): boolean {
  return n % every === 0;
}

/** My calls range filter: the /v1/me/calls `range` values, today first (the default). */
export const CALL_RANGES = [
  { key: "today", label: "Today" },
  { key: "7d", label: "7 days" },
  { key: "30d", label: "30 days" },
] as const;
export type CallRange = (typeof CALL_RANGES)[number]["key"];

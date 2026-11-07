// Pure helpers for the agent's own page (/me); kept free of Vue so they are
// unit-testable.

/** Talk time: "45s", "1m 05s", "1h 02m". */
export function fmtTalk(secs: number): string {
  const s = Math.max(0, Math.round(secs));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (s < 3600) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

/** Wait of the oldest queued caller, from open-stint timestamps (oldest first). */
export function longestWaitSeconds(waitingSince: string[] | null | undefined, nowMs: number): number {
  const t = waitingSince?.[0] ? Date.parse(waitingSince[0]) : NaN;
  return Number.isNaN(t) ? 0 : Math.max(0, Math.round((nowMs - t) / 1000));
}

/** "Queue empty" or "3 waiting, longest 1m 05s". */
export function queueSummary(waiting: number, longestSecs: number): string {
  if (waiting <= 0) return "Queue empty";
  return `${waiting} waiting, longest ${fmtTalk(longestSecs)}`;
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

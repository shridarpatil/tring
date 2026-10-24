import type { TeamStrategy } from "../api/types";

// Ring strategies a team can use, shared by the teams list and team detail views.
export const strategies: { value: TeamStrategy; label: string; hint: string }[] = [
  { value: "ring_all", label: "Ring all", hint: "Ring every available member at once" },
  { value: "round_robin", label: "Round robin", hint: "Rotate who rings first (spread load)" },
  { value: "sequential", label: "Sequential", hint: "Ring members one at a time, in order" },
  { value: "longest_wait", label: "Longest idle", hint: "Ring the member idle the longest" },
];

export const strategyOptions = strategies.map((s) => ({ value: s.value, label: s.label }));

export function strategyLabel(s: string) {
  return strategies.find((x) => x.value === s)?.label ?? s;
}

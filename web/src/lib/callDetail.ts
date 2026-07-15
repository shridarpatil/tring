import type { Call, CallEvent, IvrStep } from "../api/types";

export type EventKind = "success" | "danger" | "accent" | "muted";
export type EventIcon =
  | "flag"
  | "transfer"
  | "phone-check"
  | "phone-off"
  | "phone-missed"
  | "supervise";

export interface DetailEvent {
  time: string;
  icon: EventIcon;
  kind: EventKind;
  label: string;
  sub: string;
  failed: boolean;
}

export interface CallBreakdown {
  talkSec: number;
  ringSec: number;
  ivrSec: number;
}

export interface CallStats {
  transfers: number;
  agentsRung: number;
  missed: number;
}

export interface CallDetail {
  id: string;
  direction: string;
  status: string;
  caller: string;
  callee: string;
  startedAt: string;
  durationSec: number;
  breakdown: CallBreakdown;
  stats: CallStats;
  events: DetailEvent[];
}

// --- shared helpers ---------------------------------------------------------

// str safely reads a string field from an event's untyped detail bag.
function str(d: Record<string, unknown>, k: string): string {
  return typeof d[k] === "string" ? (d[k] as string) : "";
}

// secsBetween returns whole seconds from a→b, clamped ≥ 0.
function secsBetween(a: string, b: string): number {
  const ms = new Date(b).getTime() - new Date(a).getTime();
  return isNaN(ms) ? 0 : Math.max(0, Math.round(ms / 1000));
}

// hms renders an ISO time as "8:12:05" (local, 12-hour, no AM/PM suffix).
function hms(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const h = d.getHours() % 12 || 12;
  const m = d.getMinutes().toString().padStart(2, "0");
  const s = d.getSeconds().toString().padStart(2, "0");
  return `${h}:${m}:${s}`;
}

// ivrNav condenses the IVR walk into "greeting → pressed 1 → pressed 5".
// Transfers/hangups are excluded — they surface as their own rows.
function ivrNav(steps: IvrStep[]): string {
  const parts: string[] = [];
  for (const st of steps) {
    if (st.node_type === "greeting") parts.push("greeting");
    else if (st.node_type === "menu" || st.node_type === "gather") {
      if (st.outcome && st.outcome.startsWith("digit:")) parts.push(`pressed ${st.outcome.slice(6)}`);
      else if (st.dtmf_input) parts.push(`pressed ${st.dtmf_input}`);
    }
  }
  return parts.join(" → ");
}

const ENDED_BY: Record<string, string> = { client: "caller", agent: "agent", system: "system" };

// failedIvrTransfers are IVR transfer nodes whose target never answered.
function failedIvrTransfers(steps: IvrStep[]): IvrStep[] {
  return steps.filter((st) => st.node_type === "transfer" && st.outcome !== "completed");
}

// --- summary adapter --------------------------------------------------------

export function toCallDetail(call: Call, events: CallEvent[]): CallDetail {
  const evs = [...events].sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime());
  const ivr = call.ivr_path ?? [];
  const started = evs.find((e) => e.type === "started");
  const startAt = started?.at ?? call.started_at;
  const firstRing = evs.find((e) => e.type === "ring");

  // breakdown: IVR window before first ring; ring = Σ each ring→(answer|no_answer) gap.
  const ivrSec = firstRing ? secsBetween(startAt, firstRing.at) : 0;
  let ringSec = 0;
  let pending: CallEvent | null = null;
  for (const e of evs) {
    if (e.type === "ring") pending = e;
    else if ((e.type === "answered" || e.type === "no_answer") && pending) {
      ringSec += secsBetween(pending.at, e.at);
      pending = null;
    }
  }
  const durationSec = Math.round(call.duration_seconds ?? 0);
  const talkSec = Math.max(0, durationSec - ringSec - ivrSec);

  // stats
  const failedIvr = failedIvrTransfers(ivr);
  const transfers = evs.filter((e) => e.type === "transfer").length;
  const rung = new Set<string>();
  for (const st of failedIvr) if (st.detail) rung.add(st.detail);
  for (const e of evs) if (e.type === "ring") { const a = str(e.detail, "agent"); if (a) rung.add(a); }
  const missed = failedIvr.length + evs.filter((e) => e.type === "no_answer").length;
  const stats: CallStats = { transfers, agentsRung: rung.size, missed };

  // events stream
  const out: DetailEvent[] = [];
  if (started) {
    out.push({ time: hms(startAt), icon: "flag", kind: "muted", label: "Entered IVR", sub: ivrNav(ivr), failed: false });
  }
  // IVR-path transfers carry no timestamp → approximate "8:12:—".
  const approx = hms(startAt).replace(/:\d{2}$/, ":—");
  for (const st of failedIvr) {
    out.push({ time: approx, icon: "transfer", kind: "danger", label: `IVR transfer → ${st.detail || "agent"}`, sub: "no answer", failed: true });
  }
  pending = null;
  for (const e of evs) {
    const d = e.detail;
    switch (e.type) {
      case "ring":
        pending = e;
        break;
      case "answered": {
        const agent = str(d, "agent") || "Agent";
        out.push({ time: hms(e.at), icon: "phone-check", kind: "success", label: `${agent} answered`, sub: pending ? `rang ${secsBetween(pending.at, e.at)}s` : "", failed: false });
        pending = null;
        break;
      }
      case "transfer": {
        const to = str(d, "to");
        const by = str(d, "from");
        out.push({ time: hms(e.at), icon: "transfer", kind: "accent", label: `Transferred to ${to || "agent"}`, sub: by ? `by ${by}` : "", failed: false });
        break;
      }
      case "no_answer": {
        const t = str(d, "target");
        out.push({ time: hms(e.at), icon: "phone-missed", kind: "danger", label: `No answer${t ? ` from ${t}` : ""}`, sub: "", failed: true });
        pending = null;
        break;
      }
      case "supervise":
        out.push({ time: hms(e.at), icon: "supervise", kind: "accent", label: `Supervisor joined${str(d, "mode") ? ` (${str(d, "mode")})` : ""}`, sub: "", failed: false });
        break;
      case "ended":
        out.push({ time: hms(e.at), icon: "phone-off", kind: "muted", label: `Call ended by ${ENDED_BY[str(d, "cause")] || "system"}`, sub: "", failed: false });
        break;
    }
  }

  return {
    id: call.id,
    direction: call.direction,
    status: call.status,
    caller: call.from_number,
    callee: call.to_number,
    startedAt: startAt,
    durationSec,
    breakdown: { talkSec, ringSec, ivrSec },
    stats,
    events: out,
  };
}

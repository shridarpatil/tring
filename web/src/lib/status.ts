// Shared status → UI mappings so Dashboard, Calls, Agents, and Trunks render
// the same state identically. One source of truth for the live-state language.

type SignalState = "online" | "on_call" | "away" | "offline" | "ringing";
type BadgeTone = "neutral" | "success" | "live" | "danger" | "warning" | "accent";

// Agent presence (agents.status from FreeSWITCH).
export function agentSignalState(status?: string): SignalState {
  switch (status) {
    case "online":
      return "online";
    case "on_call":
      return "on_call";
    case "away":
      return "away";
    default:
      return "offline";
  }
}

// Call lifecycle status → badge tone.
export function callStatusTone(status: string): BadgeTone {
  switch (status) {
    case "answered":
    case "in_progress":
    case "live":
      return "live";
    case "ringing":
      return "accent";
    case "completed":
    case "ended":
      return "success";
    case "failed":
    case "busy":
    case "no_answer":
    case "canceled":
    case "cancelled":
      return "danger";
    default:
      return "neutral";
  }
}

// SIP trunk registration state → badge tone.
export function trunkStatusTone(status?: string): BadgeTone {
  switch (status) {
    case "online":
      return "success";
    case "connecting":
      return "warning";
    case "not_loaded":
    case "offline":
      return "danger";
    default:
      return "neutral";
  }
}

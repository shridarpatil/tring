export interface CampaignConfig {
  webhook_id?: string;
  type: "audio" | "tts" | "ivr";
  audio_id?: string;
  text?: string;
  flow_id?: string;
  caller_id: string;
  timezone: string;
  window_start: string;
  window_end: string;
  concurrency: number;
  calls_per_minute: number;
  max_attempts: number;
  retry_minutes: number;
  ring_seconds: number;
  max_seconds: number;
}
export interface Campaign {
  id: string;
  name: string;
  status: string;
  revision: number;
  config: CampaignConfig;
  prepared: { audio_file?: string; flow?: { nodes: { id: string; label: string; config: { audio_file?: string } }[] } } | null;
  counts: Record<string, number> | null;
  metrics: { attempts: number; answered: number; completed: number };
  scheduled_at: string | null;
  created_at: string;
}
export interface CampaignRecipient {
  id: string;
  phone: string;
  external_ref: string;
  status: string;
  attempts: number;
}
export interface CampaignAttempt {
  id: string;
  recipient_id: string | null;
  phone: string;
  external_ref: string;
  status: string;
  attempt: number;
  call_id: string | null;
  answered_at: string | null;
  created_at: string;
  error: string;
}
export function defaultCampaignConfig(): CampaignConfig {
  return { type: "audio", webhook_id: "", caller_id: "", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Kolkata", window_start: "09:00", window_end: "18:00", concurrency: 2, calls_per_minute: 30, max_attempts: 1, retry_minutes: 30, ring_seconds: 30, max_seconds: 300 };
}
export function recipientSummary(counts: Record<string, number> | null) {
  const c = counts || {};
  const total = Object.values(c).reduce((a, b) => a + b, 0);
  const pending = c.pending || 0;
  const active = c.active || 0;
  return { total, pending, active, finished: total - pending - active, completed: c.completed || 0 };
}
export function campaignLabel(value: string): string {
  const names: Record<string, string> = { audio: "Voice blast · uploaded audio", tts: "Voice blast · text-to-speech", ivr: "IVR campaign", no_answer: "No answer", unknown: "Needs review", incomplete: "Incomplete", completed: "Completed", queued: "Queued", active: "Calling", pending: "Pending", draft: "Draft", scheduled: "Scheduled", running: "Running", paused: "Paused", cancelled: "Cancelled", failed: "Failed", busy: "Busy" };
  return names[value] || value;
}
export type CampaignTone = "neutral" | "success" | "live" | "danger" | "warning" | "accent";
// Badge tone for both campaign and recipient/attempt statuses. "unknown" means a
// gateway interruption that needs a human, so it reads as a warning, not a failure.
export function campaignTone(status: string): CampaignTone {
  const tones: Record<string, CampaignTone> = { running: "live", active: "live", queued: "live", completed: "success", failed: "danger", busy: "warning", no_answer: "warning", incomplete: "warning", unknown: "warning", paused: "warning", scheduled: "accent" };
  return tones[status] || "neutral";
}

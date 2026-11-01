// Mirror of the Go API response shapes (internal/api).

// Paginated<T> is the uniform envelope every list endpoint returns (pageEnvelope
// in Go): the rows plus the total matching the query and the window that
// produced them.
export interface Paginated<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
}

export interface Tenant {
  id: string;
  name: string;
  sip_domain: string;
  outbound_strategy: string;
  failover_causes: string[];
  // System hold music: an audio asset's file_path, or "" for the built-in default.
  hold_music?: string;
  // Record a call once the agent and client connect.
  record_calls?: boolean;
  // Max call duration in seconds (also the ephemeral TURN credential lifetime).
  max_call_seconds?: number;
  // Played to the caller when a blind transfer finds no one available.
  transfer_failed_message?: string;
  // Sample rate (Hz) call recordings are encoded at: 8000/12000/16000/24000/48000.
  recording_sample_rate?: number;
  // Format of call recordings: ogg (default), mp3 or wav.
  recording_format?: string;
}

// An uploaded sound in the tenant's library, reusable for IVR prompts + hold music.
export interface Audio {
  id: string;
  tenant_id: string;
  name: string;
  file_path: string; // playback path; stored as the value in node config / hold_music
  format: "wav" | "mp3";
  size_bytes: number;
  created_at: string;
}

export interface Agent {
  id: string;
  tenant_id: string;
  sip_username: string;
  display_name: string;
  // Backend-managed availability (the integrator's Away/Break toggle). The
  // orchestrator rings an agent only when connected AND available_for_calls.
  available_for_calls: boolean;
  created_at: string;
  // Live effective state, populated on list responses: combines connection
  // presence with available_for_calls ("away" = connected but unavailable).
  status?: "online" | "offline" | "on_call" | "away" | "unknown";
}

// Ring strategies a team can use (mirrors the API's teamStrategies + the
// orchestrator's orderByStrategy).
export type TeamStrategy = "ring_all" | "round_robin" | "sequential" | "longest_wait";

// A team is a tenant-scoped group of agents the IVR transfer node rings as a
// unit. Identified by name (no id). Also its own queue when all members busy.
// One weekday of a team's working hours (same shape as the IVR Timing node).
export interface TeamDay {
  day: string;
  enabled: boolean;
  start_time?: string;
  end_time?: string;
}

export interface Team {
  tenant_id: string;
  name: string;
  strategy: TeamStrategy;
  ring_seconds: number;
  max_wait_seconds: number;
  moh_sound: string;
  min_live_agents: number; // 0 = no staffing alert
  sla_seconds: number; // 0 = no SLA
  hours: TeamDay[]; // [] = always open
  created_at: string;
  members?: TeamMember[]; // populated only on the single-team GET
}

export interface TeamMember {
  agent_id: string;
  sip_username: string;
  display_name?: string;
  available_for_calls: boolean;
  position: number;
}

// Live count of callers waiting in each team's queue (GET /v1/queues).
export interface QueueDepth {
  team: string;
  waiting: number;
  open: boolean; // inside the team's working hours
  min_live_agents: number;
  sla_seconds: number;
  hours: TeamDay[];
  members: number;
  agents: string[]; // member SIP usernames, sorted
  live: number; // on_call + available
  on_call: number;
  available: number;
  away: number;
  waiting_since: string[]; // ISO enqueue times of callers waiting, oldest first
  breaches_today: number;
  below_min_since?: string; // ISO; set while open and live < min_live_agents
}
export interface Queues {
  total: number; // callers waiting across all team queues
  on_call: number; // live calls currently connected to an agent
  queues: QueueDepth[];
}

// Dashboard "today so far" aggregates (GET /v1/stats/overview) — powers the
// cockpit trend strip.
export interface StatsHourBucket {
  hour: string; // ISO start-of-hour (UTC); render the label in local time
  count: number;
  breaches: number; // calls that waited past their team's SLA
}
export interface StatsOverview {
  calls_today: number;
  answered_pct: number; // 0–100
  abandoned_today: number; // callers who hung up while waiting
  failed_today: number; // calls that errored before connecting
  avg_wait_seconds: number; // time-to-answer (start→connect) of answered calls
  avg_talk_seconds: number;
  sla_breaches_today: number;
  avg_queue_wait_seconds: number; // time spent in team queues
  hourly: StatsHourBucket[]; // always 12 buckets, oldest→newest
}

export interface PhoneNumber {
  id: string;
  tenant_id: string;
  e164: string;
  type: string; // inbound | outbound | masking
  trunk_id: string | null;
  // Flow attached to this DID for inbound IVR routing. null = calls
  // skip the executor and fall through to the default dialplan.
  flow_id: string | null;
  provider_did_id: string;
  priority: number;
  active: boolean;
  created_at: string;
}

// IvrStep is one node the call walked through (mirrors orchestrator.SimStep).
export interface IvrStep {
  node_id: string;
  node_type: string;
  action: string;
  detail?: string;
  dtmf_input?: string;
  outcome: string;
  at?: string; // wall-clock execution time (ISO); absent on traces recorded before per-step timing
}

// CallEvent is one row of a call's durable timeline (GET /v1/calls/{id}/events).
// type ∈ started|ring|no_answer|answered|transfer|supervise|ended; detail is
// type-specific (e.g. {target}, {agent}, {from,to,result}, {cause,duration_seconds}).
export interface CallEvent {
  type: string;
  at: string;
  detail: Record<string, unknown>;
}

export interface Call {
  id: string;
  direction: string;
  status: string;
  from_number: string;
  to_number: string;
  agent_id?: string;
  agent_name?: string;
  client_ref?: string;
  source?: 'web' | 'phone' | 'whatsapp';
  // Live-dashboard call phase (event-driven, not persisted): "ivr" while the
  // caller is in the IVR, "ringing" while an agent/team is being rung. Absent on
  // rows rebuilt from a reconnect snapshot.
  phase?: 'ivr' | 'ringing';
  // Set while the caller waits in a team queue (queue.stint events).
  queue_team?: string;
  queued_at?: string;
  queue_sla_seconds?: number;
  ring_target?: string; // agent/team being rung, shown as the destination during "ringing"
  leg_id: string;
  started_at: string;
  answered_at?: string;
  ended_at?: string;
  duration_seconds?: number;
  hangup_cause?: string;
  recording_url?: string;
  ivr_path?: IvrStep[];
  room_id?: string;
  supervised?: string;
  supervisor?: string;
}

// A registered webhook that IVR flows pick by id (a node's "Notify webhook",
// a flow's "On call end"). Header values are write-only: only names return.
export interface Webhook {
  id: string;
  tenant_id: string;
  name: string;
  url: string;
  secret?: string; // only returned on create
  header_names: string[];
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface WebhookDelivery {
  id: string;
  event_type: string;
  status: string;
  attempts: number;
  response_status?: number;
  last_error?: string;
  created_at: string;
  delivered_at?: string;
}

export interface AgentToken {
  token: string;
  expires_at: string;
}

// ─── IVR / Flow types ────────────────────────────────────────────────
// Mirrors Whatomate's IVRFlowData v2 schema so flows authored in Tring
// remain interchangeable. Node config is open-ended per node type — see
// the properties panel for the exact shape per type.

export type IVRNodeType =
  | "start"
  | "greeting"
  | "menu"
  | "gather"
  | "http_callback"
  | "transfer"
  | "goto_flow"
  | "timing"
  | "hangup";

export interface IVRNodePosition {
  x: number;
  y: number;
}

export interface IVRNode {
  id: string;
  type: IVRNodeType;
  label: string;
  position: IVRNodePosition;
  config: Record<string, unknown>;
}

export interface IVREdge {
  from: string;
  to: string;
  // Source handle: "default", "digit:1..9", "timeout", "max_retries",
  // "http:2xx", "http:non2xx", "completed", "no_answer", "in_hours",
  // "out_of_hours" — depends on node type.
  condition: string;
}

export interface IVRFlowData {
  version: 2;
  nodes: IVRNode[];
  edges: IVREdge[];
  entry_node: string;
  end_webhook_id?: string; // webhook sent call.ended when a call through this flow ends
}

export interface Flow {
  id: string;
  tenant_id: string;
  name: string;
  description?: string;
  is_active: boolean;
  is_outgoing_end: boolean;
  definition: IVRFlowData;
  welcome_audio_url?: string;
  created_at: string;
  updated_at: string;
}

// Guest web-calling config (GET/PATCH /v1/guest-config). default_flow_id is the
// tenant's default IVR flow for guest calls — the fallback used only when a mint
// doesn't pass an explicit flow_id (tokens are minted backend-side via the API key).
export interface GuestConfig {
  default_flow_id: string | null;
}

// Tenant API key. `key` (plaintext) is present only in the create response,
// shown once; the list returns prefix + status only.
export interface ApiKey {
  id: string;
  name: string;
  key?: string;
  key_prefix: string;
  // "calls": calls, guest/agent tokens, agents and read-only lookups only.
  // "full": everything the console can do (key, trunk, webhook, settings admin).
  scope: "full" | "calls";
  created_at: string;
  last_used_at?: string;
  revoked: boolean;
}

// ─── SIP Trunks ──────────────────────────────────────────────────────
// Per-tenant trunk config managed in the admin console. The server
// renders these to FreeSWITCH gateway XML on every save and rescans
// the sofia external profile so changes are picked up live.

export interface Trunk {
  id: string;
  tenant_id: string;
  name: string;
  provider_type: string;
  sip_username: string;
  has_password: boolean; // sip_password is write-only: never returned
  sip_realm: string;
  register: boolean;
  proxy_ip: string;
  auth_acl_cidr: string;
  active: boolean;
  extra_params: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  // Live state from FS (sofia status). Populated on every API response.
  status?: "online" | "offline" | "connecting" | "not_loaded" | "unknown";
  state_raw?: string;
  reg_state?: string; // REGED, NOREG, FAIL_WAIT, …
  ping_ms?: number; // last OPTIONS round trip (ms); absent until answered
  // Call counters since FreeSWITCH (re)loaded the trunk. in/failed_in only
  // for registered trunks (IP-auth inbound calls aren't attributed).
  calls?: { out: number; failed_out: number; in?: number; failed_in?: number; since: string };
}

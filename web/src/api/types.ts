// Mirror of the Go API response shapes (internal/api).

export interface Tenant {
  id: string;
  name: string;
  sip_domain: string;
  outbound_strategy: string;
  failover_causes: string[];
}

export interface Agent {
  id: string;
  tenant_id: string;
  sip_username: string;
  display_name: string;
  created_at: string;
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

export interface Call {
  id: string;
  direction: string;
  status: string;
  from_number: string;
  to_number: string;
  agent_id?: string;
  fs_uuid: string;
  started_at: string;
  answered_at?: string;
  ended_at?: string;
  duration_seconds?: number;
  hangup_cause?: string;
  recording_url?: string;
}

export interface Webhook {
  id: string;
  tenant_id: string;
  url: string;
  secret?: string; // only returned on create
  events: string[];
  created_at: string;
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
  sip_password?: string; // only returned on create / get
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
}

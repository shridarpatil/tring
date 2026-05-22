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
  provider: string;
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

CREATE TABLE campaigns (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','scheduled','running','paused','cancelled','completed','failed')),
    config jsonb NOT NULL,
    prepared jsonb,
    revision integer NOT NULL DEFAULT 1,
    scheduled_at timestamptz,
    next_start_at timestamptz NOT NULL DEFAULT now(),
    next_dispatch_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id)
);
CREATE TABLE campaign_recipients (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    campaign_id uuid NOT NULL,
    phone text NOT NULL,
    external_ref text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','completed','incomplete','busy','no_answer','failed','unknown','cancelled')),
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, campaign_id) REFERENCES campaigns(tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, campaign_id, phone),
    UNIQUE (tenant_id, id)
);
CREATE TABLE campaign_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    campaign_id uuid NOT NULL,
    recipient_id uuid,
    phone text NOT NULL,
    external_ref text NOT NULL DEFAULT '',
    attempt integer NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','active','completed','incomplete','busy','no_answer','failed','unknown','cancelled')),
    payload jsonb NOT NULL,
    call_id uuid REFERENCES calls(id) ON DELETE SET NULL,
    started_at timestamptz,
    answered_at timestamptz,
    finished_at timestamptz,
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    error text NOT NULL DEFAULT '',
    FOREIGN KEY (tenant_id, campaign_id) REFERENCES campaigns(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, recipient_id) REFERENCES campaign_recipients(tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, recipient_id, attempt)
);
CREATE INDEX campaign_due ON campaigns(tenant_id, next_dispatch_at) WHERE status IN ('running','scheduled');
CREATE INDEX campaign_recipient_due ON campaign_recipients(tenant_id, campaign_id, next_attempt_at) WHERE status = 'pending';
CREATE INDEX campaign_attempt_active ON campaign_attempts(tenant_id, heartbeat_at) WHERE status IN ('queued','active');
CREATE INDEX campaign_attempt_history ON campaign_attempts(tenant_id, campaign_id, created_at DESC);

CREATE INDEX campaign_attempt_created ON campaign_attempts(tenant_id, created_at);
CREATE INDEX campaign_attempt_started ON campaign_attempts(tenant_id, started_at) WHERE started_at IS NOT NULL;

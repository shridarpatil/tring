-- Tring database schema — GENERATED SNAPSHOT. NOT the source of truth.
--
-- The authoritative schema is internal/migrate/migrations/*.sql, applied by
-- golang-migrate on cmd/api startup (idempotent, tracked in schema_migrations).
-- Nothing loads this file at runtime; it exists only as a human-readable
-- reference for the schema at HEAD.
--
-- Regenerate after adding a migration (against a DB migrated to HEAD):
--   PGPASSWORD=tring_dev pg_dump -h localhost -p 5434 -U tring -d tring_test \
--     --schema-only --no-owner --no-privileges | (strip the SET/\restrict preamble)

--
--

--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

--
-- Name: EXTENSION pgcrypto; Type: COMMENT; Schema: -; Owner: -
--

--
-- Name: call_direction; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE call_direction AS ENUM (
    'inbound',
    'outbound'
);

--
-- Name: call_source; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE call_source AS ENUM (
    'web',
    'phone',
    'whatsapp'
);

--
-- Name: call_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE call_status AS ENUM (
    'ringing',
    'answered',
    'ended',
    'failed'
);

--
-- Name: number_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE number_type AS ENUM (
    'inbound',
    'outbound',
    'masking'
);

--
-- Name: webhook_delivery_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE webhook_delivery_status AS ENUM (
    'pending',
    'success',
    'exhausted'
);

--
-- Name: agents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE agents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    sip_username text NOT NULL,
    sip_password text NOT NULL,
    display_name text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    available_for_calls boolean DEFAULT true NOT NULL
);

--
-- Name: api_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE api_keys (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    key_hash text NOT NULL,
    key_prefix text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone
);

--
-- Name: audio_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE audio_assets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    file_path text NOT NULL,
    format text NOT NULL,
    size_bytes bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: calls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE calls (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    direction call_direction NOT NULL,
    status call_status NOT NULL,
    from_number text NOT NULL,
    to_number text NOT NULL,
    agent_id uuid,
    leg_id text,
    job_uuid text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    answered_at timestamp with time zone,
    ended_at timestamp with time zone,
    duration_seconds integer,
    hangup_cause text,
    recording_url text,
    ivr_path jsonb DEFAULT '[]'::jsonb NOT NULL,
    client_ref text,
    recording_s3_key text,
    agent_leg_id text,
    customer_leg_id text,
    source call_source DEFAULT 'phone'::call_source NOT NULL
);

--
-- Name: flows; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE flows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    is_outgoing_end boolean DEFAULT false NOT NULL,
    definition jsonb DEFAULT '{"edges": [], "nodes": [], "version": 2, "entry_node": ""}'::jsonb NOT NULL,
    welcome_audio_url text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: masking_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE masking_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    masked_did text NOT NULL,
    party_a text NOT NULL,
    party_b text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

--
-- Name: numbers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE numbers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    e164 text NOT NULL,
    type number_type NOT NULL,
    provider text,
    provider_did_id text,
    priority integer DEFAULT 100 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    flow_id uuid,
    trunk_id uuid
);

--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE schema_migrations (
    version bigint NOT NULL,
    dirty boolean NOT NULL
);

--
-- Name: team_agents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE team_agents (
    tenant_id uuid NOT NULL,
    team_name text NOT NULL,
    agent_id uuid NOT NULL,
    "position" integer DEFAULT 1 NOT NULL
);

--
-- Name: teams; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE teams (
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    strategy text DEFAULT 'ring_all'::text NOT NULL,
    ring_seconds integer DEFAULT 20 NOT NULL,
    max_wait_seconds integer DEFAULT 120 NOT NULL,
    moh_sound text DEFAULT 'local_stream://moh'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: tenants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE tenants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    api_key_hash text NOT NULL,
    api_key_prefix text NOT NULL,
    sip_domain text NOT NULL,
    outbound_strategy text DEFAULT 'failover'::text NOT NULL,
    failover_causes text[] DEFAULT ARRAY['NETWORK_OUT_OF_ORDER'::text, 'GATEWAY_DOWN'::text, 'USER_NOT_REGISTERED'::text, 'SUBSCRIBER_ABSENT'::text, 'NORMAL_TEMPORARY_FAILURE'::text, 'RECOVERY_ON_TIMER_EXPIRE'::text, 'NO_ROUTE_DESTINATION'::text] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    default_flow_id uuid,
    hold_music text DEFAULT ''::text NOT NULL,
    record_calls boolean DEFAULT true NOT NULL,
    max_call_seconds integer DEFAULT 14400 NOT NULL
);

--
-- Name: trunks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE trunks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    provider_type text DEFAULT 'generic'::text NOT NULL,
    sip_username text DEFAULT ''::text NOT NULL,
    sip_password text DEFAULT ''::text NOT NULL,
    sip_realm text DEFAULT ''::text NOT NULL,
    register boolean DEFAULT false NOT NULL,
    proxy_ip text DEFAULT ''::text NOT NULL,
    auth_acl_cidr text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    extra_params jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: webhook_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE webhook_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    webhook_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    status webhook_delivery_status DEFAULT 'pending'::webhook_delivery_status NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text,
    response_status integer,
    delivered_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: webhooks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE webhooks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    url text NOT NULL,
    secret text NOT NULL,
    events text[] DEFAULT '{}'::text[] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: agents agents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY agents
    ADD CONSTRAINT agents_pkey PRIMARY KEY (id);

--
-- Name: agents agents_tenant_id_sip_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY agents
    ADD CONSTRAINT agents_tenant_id_sip_username_key UNIQUE (tenant_id, sip_username);

--
-- Name: api_keys api_keys_key_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY api_keys
    ADD CONSTRAINT api_keys_key_hash_key UNIQUE (key_hash);

--
-- Name: api_keys api_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);

--
-- Name: audio_assets audio_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY audio_assets
    ADD CONSTRAINT audio_assets_pkey PRIMARY KEY (id);

--
-- Name: audio_assets audio_assets_tenant_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY audio_assets
    ADD CONSTRAINT audio_assets_tenant_id_name_key UNIQUE (tenant_id, name);

--
-- Name: calls calls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY calls
    ADD CONSTRAINT calls_pkey PRIMARY KEY (id);

--
-- Name: flows flows_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY flows
    ADD CONSTRAINT flows_pkey PRIMARY KEY (id);

--
-- Name: masking_sessions masking_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY masking_sessions
    ADD CONSTRAINT masking_sessions_pkey PRIMARY KEY (id);

--
-- Name: masking_sessions masking_sessions_tenant_id_masked_did_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY masking_sessions
    ADD CONSTRAINT masking_sessions_tenant_id_masked_did_key UNIQUE (tenant_id, masked_did);

--
-- Name: numbers numbers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY numbers
    ADD CONSTRAINT numbers_pkey PRIMARY KEY (id);

--
-- Name: numbers numbers_tenant_id_e164_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY numbers
    ADD CONSTRAINT numbers_tenant_id_e164_key UNIQUE (tenant_id, e164);

--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);

--
-- Name: team_agents team_agents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY team_agents
    ADD CONSTRAINT team_agents_pkey PRIMARY KEY (tenant_id, team_name, agent_id);

--
-- Name: teams teams_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY teams
    ADD CONSTRAINT teams_pkey PRIMARY KEY (tenant_id, name);

--
-- Name: tenants tenants_api_key_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY tenants
    ADD CONSTRAINT tenants_api_key_hash_key UNIQUE (api_key_hash);

--
-- Name: tenants tenants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (id);

--
-- Name: tenants tenants_sip_domain_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY tenants
    ADD CONSTRAINT tenants_sip_domain_key UNIQUE (sip_domain);

--
-- Name: trunks trunks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY trunks
    ADD CONSTRAINT trunks_pkey PRIMARY KEY (id);

--
-- Name: users users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY users
    ADD CONSTRAINT users_email_key UNIQUE (email);

--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: webhook_deliveries webhook_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_pkey PRIMARY KEY (id);

--
-- Name: webhooks webhooks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY webhooks
    ADD CONSTRAINT webhooks_pkey PRIMARY KEY (id);

--
-- Name: api_keys_tenant_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX api_keys_tenant_id_idx ON api_keys USING btree (tenant_id);

--
-- Name: calls_job_uuid_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX calls_job_uuid_idx ON calls USING btree (job_uuid);

--
-- Name: calls_leg_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX calls_leg_id_idx ON calls USING btree (leg_id);

--
-- Name: calls_tenant_id_agent_id_started_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX calls_tenant_id_agent_id_started_at_idx ON calls USING btree (tenant_id, agent_id, started_at DESC);

--
-- Name: calls_tenant_id_started_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX calls_tenant_id_started_at_idx ON calls USING btree (tenant_id, started_at DESC);

--
-- Name: flows_tenant_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX flows_tenant_name_idx ON flows USING btree (tenant_id, name);

--
-- Name: idx_users_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_tenant ON users USING btree (tenant_id);

--
-- Name: masking_sessions_tenant_id_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX masking_sessions_tenant_id_expires_at_idx ON masking_sessions USING btree (tenant_id, expires_at);

--
-- Name: numbers_tenant_id_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX numbers_tenant_id_type_idx ON numbers USING btree (tenant_id, type);

--
-- Name: numbers_tenant_id_type_priority_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX numbers_tenant_id_type_priority_idx ON numbers USING btree (tenant_id, type, priority) WHERE active;

--
-- Name: trunks_tenant_name_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX trunks_tenant_name_unique ON trunks USING btree (tenant_id, name);

--
-- Name: webhook_deliveries_webhook_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_deliveries_webhook_id_created_at_idx ON webhook_deliveries USING btree (webhook_id, created_at DESC);

--
-- Name: agents agents_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY agents
    ADD CONSTRAINT agents_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: api_keys api_keys_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY api_keys
    ADD CONSTRAINT api_keys_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: audio_assets audio_assets_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY audio_assets
    ADD CONSTRAINT audio_assets_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: calls calls_agent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY calls
    ADD CONSTRAINT calls_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE SET NULL;

--
-- Name: calls calls_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY calls
    ADD CONSTRAINT calls_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: flows flows_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY flows
    ADD CONSTRAINT flows_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: masking_sessions masking_sessions_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY masking_sessions
    ADD CONSTRAINT masking_sessions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: numbers numbers_flow_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY numbers
    ADD CONSTRAINT numbers_flow_id_fkey FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE SET NULL;

--
-- Name: numbers numbers_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY numbers
    ADD CONSTRAINT numbers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: numbers numbers_trunk_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY numbers
    ADD CONSTRAINT numbers_trunk_id_fkey FOREIGN KEY (trunk_id) REFERENCES trunks(id) ON DELETE SET NULL;

--
-- Name: team_agents team_agents_agent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY team_agents
    ADD CONSTRAINT team_agents_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;

--
-- Name: team_agents team_agents_tenant_id_team_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY team_agents
    ADD CONSTRAINT team_agents_tenant_id_team_name_fkey FOREIGN KEY (tenant_id, team_name) REFERENCES teams(tenant_id, name) ON DELETE CASCADE;

--
-- Name: teams teams_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY teams
    ADD CONSTRAINT teams_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: tenants tenants_support_flow_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY tenants
    ADD CONSTRAINT tenants_support_flow_id_fkey FOREIGN KEY (default_flow_id) REFERENCES flows(id) ON DELETE SET NULL;

--
-- Name: trunks trunks_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY trunks
    ADD CONSTRAINT trunks_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: users users_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY users
    ADD CONSTRAINT users_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
-- Name: webhook_deliveries webhook_deliveries_webhook_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_webhook_id_fkey FOREIGN KEY (webhook_id) REFERENCES webhooks(id) ON DELETE CASCADE;

--
-- Name: webhooks webhooks_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY webhooks
    ADD CONSTRAINT webhooks_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

--
--


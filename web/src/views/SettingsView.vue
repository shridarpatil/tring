<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import AudioPicker from "../components/AudioPicker.vue";
import { Card, Button, Select, Toggle, Badge } from "../components/ui";
import type { Tenant, GuestConfig, Flow, Paginated } from "../api/types";

const tenant = ref<Tenant | null>(null);
const loading = ref(true);
const saving = ref(false);

const strategy = ref("failover");
const strategies = ["failover", "round_robin", "manual", "load_based"];
const strategyOptions = strategies.map((s) => ({ value: s, label: s }));
// System hold music (an uploaded sound's file_path; "" = built-in default).
const holdMusic = ref("");
// Record a call once the agent and client connect.
const recordCalls = ref(true);
// Max call duration in MINUTES (UI); stored as seconds on the tenant.
const maxCallMinutes = ref(240);

// ─── Guest web-calling config (the tenant's default guest flow) ───────
const guest = ref<GuestConfig | null>(null);
const flows = ref<Flow[]>([]);
const defaultFlowId = ref("");
const guestSaving = ref(false);

const activeFlows = computed(() => flows.value.filter((f) => f.is_active));
const flowOptions = computed(() => [
  { value: "", label: "— none (backend must pass flow_id) —" },
  ...activeFlows.value.map((f) => ({ value: f.id, label: f.name })),
]);

async function load() {
  loading.value = true;
  try {
    tenant.value = await api.get<Tenant>("/v1/tenants/me");
    strategy.value = tenant.value.outbound_strategy || "failover";
    holdMusic.value = tenant.value.hold_music || "";
    recordCalls.value = tenant.value.record_calls ?? true;
    maxCallMinutes.value = Math.round((tenant.value.max_call_seconds ?? 14400) / 60);
  } catch (e) {
    toast.error(errMessage(e, "failed to load"));
  } finally {
    loading.value = false;
  }
  try {
    const [g, fl] = await Promise.all([
      api.get<GuestConfig>("/v1/guest-config"),
      api.get<Paginated<Flow>>("/v1/flows"),
    ]);
    guest.value = g;
    flows.value = fl.items;
    defaultFlowId.value = g.default_flow_id || "";
  } catch (e) {
    toast.error(errMessage(e, "failed to load guest config"));
  }
}

async function save() {
  saving.value = true;
  try {
    tenant.value = await api.patch<Tenant>("/v1/tenants/me", {
      outbound_strategy: strategy.value,
      hold_music: holdMusic.value,
      record_calls: recordCalls.value,
      max_call_seconds: Math.round(maxCallMinutes.value * 60),
    });
    holdMusic.value = tenant.value.hold_music || "";
    recordCalls.value = tenant.value.record_calls ?? true;
    maxCallMinutes.value = Math.round((tenant.value.max_call_seconds ?? 14400) / 60);
    toast.success("Saved");
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  } finally {
    saving.value = false;
  }
}

async function saveGuest() {
  guestSaving.value = true;
  try {
    guest.value = await api.patch<GuestConfig>("/v1/guest-config", {
      default_flow_id: defaultFlowId.value,
    });
    defaultFlowId.value = guest.value.default_flow_id || "";
    toast.success("Saved");
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  } finally {
    guestSaving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="space-y-5 max-w-2xl">
    <h1 class="text-xl font-semibold tracking-tight">Settings</h1>

    <Card v-if="tenant" class="space-y-5">
      <div class="grid grid-cols-2 gap-4 text-sm">
        <div>
          <div class="text-ink-muted">Tenant</div>
          <div class="font-medium text-ink">{{ tenant.name }}</div>
        </div>
        <div>
          <div class="text-ink-muted">SIP domain</div>
          <div class="font-mono text-ink">{{ tenant.sip_domain }}</div>
        </div>
      </div>

      <div>
        <Select
          v-model="strategy"
          label="Outbound trunk strategy"
          :options="strategyOptions"
        />
        <p class="text-xs text-ink-faint mt-1">
          failover = try all in priority order · round_robin = rotate · manual = lowest priority only · load_based = reserved
        </p>
      </div>

      <div v-if="tenant.failover_causes?.length" class="text-sm">
        <div class="text-ink-muted">Failover causes</div>
        <div class="mt-1 flex flex-wrap gap-1">
          <Badge v-for="c in tenant.failover_causes" :key="c" tone="neutral">{{ c }}</Badge>
        </div>
      </div>

      <div>
        <span class="text-xs font-medium text-ink-muted">Hold music</span>
        <p class="text-xs text-ink-faint mb-1">
          Played to callers waiting in a team's queue. Upload audio under <strong>Sounds</strong>.
          Default is FreeSWITCH's built-in music.
        </p>
        <AudioPicker v-model="holdMusic" default-label="Default (built-in music)" />
      </div>

      <div>
        <Toggle v-model="recordCalls" label="Record calls" />
        <p class="text-xs text-ink-faint mt-1">
          Record each call once the agent and caller connect. Recordings appear in the
          <strong>Calls</strong> log. Make sure recording is permitted and disclosed where you operate.
        </p>
      </div>

      <div>
        <span class="text-xs font-medium text-ink-muted">Max call duration (minutes)</span>
        <input
          v-model.number="maxCallMinutes"
          type="number"
          min="1"
          max="1440"
          class="mt-1 block w-40 rounded-md bg-surface-2 border border-border px-3 h-9 text-sm text-ink transition-colors focus:outline-none focus:ring-2 focus:ring-accent/40 focus:border-accent"
        />
        <p class="text-xs text-ink-faint mt-1">
          Longest a call may last. Also sets the TURN relay credential lifetime, so it must
          cover your longest call. 1–1440 min.
        </p>
      </div>

      <div>
        <Button :loading="saving" @click="save">Save changes</Button>
      </div>
    </Card>

    <!-- ─── Guest web calling ─────────────────────────────────────── -->
    <Card v-if="guest" class="space-y-5">
      <div>
        <h2 class="text-base font-semibold text-ink">Guest web calling</h2>
        <p class="text-sm text-ink-muted">
          The default IVR flow a guest call lands in when your backend doesn't pass
          an explicit <code>flow_id</code>. Visitor tokens are minted server-side via
          <code>POST /v1/guest-credentials</code> (your API key) — gate that with a
          CAPTCHA for anonymous guests.
        </p>
      </div>

      <div>
        <Select v-model="defaultFlowId" label="Default flow" :options="flowOptions" />
        <p class="text-xs text-ink-faint mt-1">
          The fallback flow for guest calls with no explicit flow_id. Only active flows are listed.
        </p>
      </div>

      <div>
        <Button :loading="guestSaving" @click="saveGuest">Save guest config</Button>
      </div>
    </Card>
  </div>
</template>

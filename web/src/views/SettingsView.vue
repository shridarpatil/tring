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
// Sample rate (Hz) the recording is encoded at; a string because <Select> emits one.
const recordingSampleRate = ref("8000");
const sampleRateOptions = [
  { value: "8000", label: "8 kHz (phone quality, smallest files)" },
  { value: "12000", label: "12 kHz" },
  { value: "16000", label: "16 kHz (wideband)" },
  { value: "24000", label: "24 kHz" },
  { value: "48000", label: "48 kHz (full quality, largest files)" },
];
// Max call duration in MINUTES (UI); stored as seconds on the tenant.
const maxCallMinutes = ref(240);
// Played to the caller when a blind transfer finds no one available.
const transferFailedMessage = ref("");

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
    recordingSampleRate.value = String(tenant.value.recording_sample_rate ?? 8000);
    maxCallMinutes.value = Math.round((tenant.value.max_call_seconds ?? 14400) / 60);
    transferFailedMessage.value = tenant.value.transfer_failed_message || "";
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
      recording_sample_rate: Number(recordingSampleRate.value),
      max_call_seconds: Math.round(maxCallMinutes.value * 60),
      transfer_failed_message: transferFailedMessage.value,
    });
    holdMusic.value = tenant.value.hold_music || "";
    recordCalls.value = tenant.value.record_calls ?? true;
    recordingSampleRate.value = String(tenant.value.recording_sample_rate ?? 8000);
    maxCallMinutes.value = Math.round((tenant.value.max_call_seconds ?? 14400) / 60);
    transferFailedMessage.value = tenant.value.transfer_failed_message || "";
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
  <div class="view" style="max-width: 42rem">
    <h1 class="page-title">Settings</h1>

    <Card v-if="tenant" class="vstack">
      <div class="grid grid-2">
        <div>
          <div class="text-light">Tenant</div>
          <div style="font-weight: 500">{{ tenant.name }}</div>
        </div>
        <div>
          <div class="text-light">SIP domain</div>
          <div class="mono">{{ tenant.sip_domain }}</div>
        </div>
      </div>

      <div>
        <Select
          v-model="strategy"
          label="Outbound trunk strategy"
          :options="strategyOptions"
        />
        <p class="text-lighter" style="margin-top: 0.5rem">
          failover = try all in priority order · round_robin = rotate · manual = lowest priority only · load_based = reserved
        </p>
      </div>

      <div v-if="tenant.failover_causes?.length">
        <div class="text-light">Failover causes</div>
        <div class="chip-wrap" style="margin-top: 0.5rem">
          <Badge v-for="c in tenant.failover_causes" :key="c" tone="neutral">{{ c }}</Badge>
        </div>
      </div>

      <div>
        <span class="text-light" style="font-weight: 500">Hold music</span>
        <p class="text-lighter" style="margin-bottom: 0.5rem">
          Played to callers waiting in a team's queue. Upload audio under <strong>Sounds</strong>.
          Default is FreeSWITCH's built-in music.
        </p>
        <AudioPicker v-model="holdMusic" default-label="Default (built-in music)" />
      </div>

      <div>
        <Toggle v-model="recordCalls" label="Record calls" />
        <p class="text-lighter" style="margin-top: 0.5rem">
          Record each call once the agent and caller connect. Recordings appear in the
          <strong>Calls</strong> log. Make sure recording is permitted and disclosed where you operate.
        </p>
      </div>

      <div>
        <Select
          v-model="recordingSampleRate"
          label="Recording quality"
          :options="sampleRateOptions"
        />
        <p class="text-lighter" style="margin-top: 0.5rem">
          Sample rate of the saved recording. Phone calls carry 8 kHz audio, so higher rates only
          help browser and WhatsApp calls. Applies to calls that start after you save.
        </p>
      </div>

      <div data-field>
        <label for="max-call-minutes">Max call duration (minutes)</label>
        <input
          id="max-call-minutes"
          v-model.number="maxCallMinutes"
          type="number"
          min="1"
          max="1440"
          style="max-width: 10rem"
        />
        <small data-hint>
          Longest a call may last. Also sets the TURN relay credential lifetime, so it must
          cover your longest call. 1–1440 min.
        </small>
      </div>

      <div data-field>
        <label for="transfer-failed-message">Transfer — no-answer message</label>
        <textarea id="transfer-failed-message" v-model="transferFailedMessage" rows="3"></textarea>
        <small data-hint>
          Played to the caller when a blind transfer finds no one available.
        </small>
      </div>

      <div>
        <Button :loading="saving" @click="save">Save changes</Button>
      </div>
    </Card>

    <!-- ─── Guest web calling ─────────────────────────────────────── -->
    <Card v-if="guest" class="vstack">
      <div>
        <h2 style="font-size: var(--text-4); margin: 0">Guest web calling</h2>
        <p class="text-light">
          The default IVR flow a guest call lands in when your backend doesn't pass
          an explicit <code>flow_id</code>. Visitor tokens are minted server-side via
          <code>POST /v1/guest-credentials</code> (your API key) — gate that with a
          CAPTCHA for anonymous guests.
        </p>
      </div>

      <div>
        <Select v-model="defaultFlowId" label="Default flow" :options="flowOptions" />
        <p class="text-lighter" style="margin-top: 0.5rem">
          The fallback flow for guest calls with no explicit flow_id. Only active flows are listed.
        </p>
      </div>

      <div>
        <Button :loading="guestSaving" @click="saveGuest">Save guest config</Button>
      </div>
    </Card>
  </div>
</template>

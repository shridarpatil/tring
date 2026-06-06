<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { api } from "../api/client";
import AudioPicker from "../components/AudioPicker.vue";
import type { Tenant, GuestConfig, Flow } from "../api/types";

const tenant = ref<Tenant | null>(null);
const loading = ref(true);
const saving = ref(false);
const error = ref("");
const saved = ref(false);

const strategy = ref("failover");
const strategies = ["failover", "round_robin", "manual", "load_based"];
// System hold music (an uploaded sound's fs_path; "" = built-in default).
const holdMusic = ref("");

// ─── Guest web-calling config (the tenant's default guest flow) ───────
const guest = ref<GuestConfig | null>(null);
const flows = ref<Flow[]>([]);
const defaultFlowId = ref("");
const guestSaving = ref(false);
const guestSaved = ref(false);
const guestError = ref("");

const activeFlows = computed(() => flows.value.filter((f) => f.is_active));

async function load() {
  loading.value = true;
  try {
    tenant.value = await api.get<Tenant>("/v1/tenants/me");
    strategy.value = tenant.value.outbound_strategy || "failover";
    holdMusic.value = tenant.value.hold_music || "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : "failed to load";
  } finally {
    loading.value = false;
  }
  try {
    const [g, fl] = await Promise.all([
      api.get<GuestConfig>("/v1/guest-config"),
      api.get<Flow[]>("/v1/flows"),
    ]);
    guest.value = g;
    flows.value = fl;
    defaultFlowId.value = g.default_flow_id || "";
  } catch (e) {
    guestError.value = e instanceof Error ? e.message : "failed to load guest config";
  }
}

async function save() {
  saving.value = true;
  error.value = "";
  saved.value = false;
  try {
    tenant.value = await api.patch<Tenant>("/v1/tenants/me", {
      outbound_strategy: strategy.value,
      hold_music: holdMusic.value,
    });
    holdMusic.value = tenant.value.hold_music || "";
    saved.value = true;
  } catch (e) {
    error.value = e instanceof Error ? e.message : "save failed";
  } finally {
    saving.value = false;
  }
}

async function saveGuest() {
  guestSaving.value = true;
  guestError.value = "";
  guestSaved.value = false;
  try {
    guest.value = await api.patch<GuestConfig>("/v1/guest-config", {
      default_flow_id: defaultFlowId.value,
    });
    defaultFlowId.value = guest.value.default_flow_id || "";
    guestSaved.value = true;
  } catch (e) {
    guestError.value = e instanceof Error ? e.message : "save failed";
  } finally {
    guestSaving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="space-y-5 max-w-2xl">
    <h1 class="text-2xl font-semibold">Settings</h1>
    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>

    <div v-if="tenant" class="bg-white rounded-xl border p-6 space-y-5">
      <div class="grid grid-cols-2 gap-4 text-sm">
        <div>
          <div class="text-gray-500">Tenant</div>
          <div class="font-medium">{{ tenant.name }}</div>
        </div>
        <div>
          <div class="text-gray-500">SIP domain</div>
          <div class="font-mono">{{ tenant.sip_domain }}</div>
        </div>
      </div>

      <label class="block">
        <span class="text-sm font-medium text-gray-700">Outbound trunk strategy</span>
        <select v-model="strategy" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option v-for="s in strategies" :key="s" :value="s">{{ s }}</option>
        </select>
        <span class="text-xs text-gray-400">
          failover = try all in priority order · round_robin = rotate · manual = lowest priority only · load_based = reserved
        </span>
      </label>

      <div v-if="tenant.failover_causes?.length" class="text-sm">
        <div class="text-gray-500">Failover causes</div>
        <div class="mt-1 flex flex-wrap gap-1">
          <span v-for="c in tenant.failover_causes" :key="c" class="bg-gray-100 rounded px-2 py-0.5 text-xs font-mono">{{ c }}</span>
        </div>
      </div>

      <div class="block">
        <span class="text-sm font-medium text-gray-700">Hold music</span>
        <p class="text-xs text-gray-400 mb-1">
          Played to callers waiting in a team's queue. Upload audio under <strong>Sounds</strong>.
          Default is FreeSWITCH's built-in music.
        </p>
        <AudioPicker v-model="holdMusic" default-label="Default (built-in music)" />
      </div>

      <div class="flex items-center gap-3">
        <button :disabled="saving" class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50" @click="save">
          {{ saving ? "Saving…" : "Save" }}
        </button>
        <span v-if="saved" class="text-sm text-green-600">Saved</span>
      </div>
    </div>

    <!-- ─── Guest web calling ─────────────────────────────────────── -->
    <div v-if="guest" class="bg-white rounded-xl border p-6 space-y-5">
      <div>
        <h2 class="text-lg font-semibold">Guest web calling</h2>
        <p class="text-sm text-gray-500">
          The default IVR flow a guest call lands in when your backend doesn't pass
          an explicit <code>flow_id</code>. Visitor tokens are minted server-side via
          <code>POST /v1/guest-credentials</code> (your API key) — gate that with a
          CAPTCHA for anonymous guests.
        </p>
      </div>

      <p v-if="guestError" class="text-sm text-red-600">{{ guestError }}</p>

      <!-- Default flow -->
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Default flow</span>
        <select v-model="defaultFlowId" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option value="">— none (backend must pass flow_id) —</option>
          <option v-for="f in activeFlows" :key="f.id" :value="f.id">{{ f.name }}</option>
        </select>
        <span class="text-xs text-gray-400">The fallback flow for guest calls with no explicit flow_id. Only active flows are listed.</span>
      </label>

      <div class="flex items-center gap-3">
        <button :disabled="guestSaving" class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50" @click="saveGuest">
          {{ guestSaving ? "Saving…" : "Save guest config" }}
        </button>
        <span v-if="guestSaved" class="text-sm text-green-600">Saved</span>
      </div>
    </div>
  </div>
</template>

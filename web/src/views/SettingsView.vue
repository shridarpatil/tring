<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { api } from "../api/client";
import type { Tenant, GuestConfig, Flow } from "../api/types";

const tenant = ref<Tenant | null>(null);
const loading = ref(true);
const saving = ref(false);
const error = ref("");
const saved = ref(false);

const strategy = ref("failover");
const strategies = ["failover", "round_robin", "manual", "load_based"];

// ─── Guest web-calling config ─────────────────────────────────────────
const guest = ref<GuestConfig | null>(null);
const flows = ref<Flow[]>([]);
const allowedOrigins = ref<string[]>([]);
const newOrigin = ref("");
const supportFlowId = ref("");
const guestSaving = ref(false);
const guestSaved = ref(false);
const guestError = ref("");

const activeFlows = computed(() => flows.value.filter((f) => f.is_active));

const embedSnippet = computed(() => {
  const origin = window.location.origin;
  const pk = guest.value?.public_key || "pk_live_…";
  return (
    `<script src="${origin}/sdk/voice.js"\n` +
    `  data-api-base="${origin}"\n` +
    `  data-public-key="${pk}"><\/script>`
  );
});

// normalizeOrigin mirrors the server: http(s)://host[:port], no path.
function normalizeOrigin(s: string): string | null {
  try {
    const u = new URL(s.trim());
    if (
      (u.protocol !== "http:" && u.protocol !== "https:") ||
      !u.host ||
      (u.pathname && u.pathname !== "/")
    ) {
      return null;
    }
    return `${u.protocol}//${u.host}`;
  } catch {
    return null;
  }
}

function addOrigin() {
  const n = normalizeOrigin(newOrigin.value);
  if (!n) {
    guestError.value = "Enter a valid origin, e.g. https://support.acme.com";
    return;
  }
  if (!allowedOrigins.value.includes(n)) allowedOrigins.value.push(n);
  newOrigin.value = "";
  guestError.value = "";
}

function removeOrigin(o: string) {
  allowedOrigins.value = allowedOrigins.value.filter((x) => x !== o);
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    /* clipboard may be unavailable; no-op */
  }
}

async function load() {
  loading.value = true;
  try {
    tenant.value = await api.get<Tenant>("/v1/tenants/me");
    strategy.value = tenant.value.outbound_strategy || "failover";
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
    allowedOrigins.value = [...g.allowed_origins];
    supportFlowId.value = g.support_flow_id || "";
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
    });
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
      support_flow_id: supportFlowId.value,
      allowed_origins: allowedOrigins.value,
    });
    allowedOrigins.value = [...guest.value.allowed_origins];
    supportFlowId.value = guest.value.support_flow_id || "";
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
          Let website visitors call into an IVR flow over the internet. Embed the snippet
          below on the pages you list under <strong>Allowed domains</strong>.
        </p>
      </div>

      <p v-if="guestError" class="text-sm text-red-600">{{ guestError }}</p>

      <!-- Public key -->
      <div>
        <span class="text-sm font-medium text-gray-700">Public widget key</span>
        <div class="mt-1 flex items-center gap-2">
          <code class="flex-1 bg-gray-50 border rounded-md px-3 py-2 text-xs font-mono break-all">{{ guest.public_key }}</code>
          <button class="text-xs border rounded-md px-2 py-2 hover:bg-gray-50" @click="copy(guest.public_key)">Copy</button>
        </div>
        <span class="text-xs text-gray-400">Safe to embed publicly — it only works from your allowed domains.</span>
      </div>

      <!-- Support flow -->
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Support flow</span>
        <select v-model="supportFlowId" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option value="">— none (guest calling disabled) —</option>
          <option v-for="f in activeFlows" :key="f.id" :value="f.id">{{ f.name }}</option>
        </select>
        <span class="text-xs text-gray-400">The IVR flow a guest call lands in. Only active flows are listed.</span>
      </label>

      <!-- Allowed origins -->
      <div>
        <span class="text-sm font-medium text-gray-700">Allowed domains</span>
        <p class="text-xs text-gray-400 mb-2">
          The widget only mints calls when loaded from one of these origins (scheme + host + port).
          This is what stops a stolen key from being embedded elsewhere.
        </p>
        <div v-if="allowedOrigins.length" class="flex flex-wrap gap-1 mb-2">
          <span v-for="o in allowedOrigins" :key="o" class="inline-flex items-center gap-1 bg-gray-100 rounded px-2 py-0.5 text-xs font-mono">
            {{ o }}
            <button class="text-gray-400 hover:text-red-600" title="Remove" @click="removeOrigin(o)">×</button>
          </span>
        </div>
        <p v-else class="text-xs text-amber-600 mb-2">
          ⚠ No domains allowed — guest calling is disabled until you add one (fail-closed).
        </p>
        <div class="flex items-center gap-2">
          <input
            v-model="newOrigin"
            placeholder="https://support.acme.com"
            class="flex-1 rounded-md border border-gray-300 px-3 py-2 text-sm"
            @keyup.enter="addOrigin"
          />
          <button class="text-sm border rounded-md px-3 py-2 hover:bg-gray-50" @click="addOrigin">Add</button>
        </div>
      </div>

      <!-- Embed snippet -->
      <div>
        <span class="text-sm font-medium text-gray-700">Embed snippet</span>
        <div class="mt-1 relative">
          <pre class="bg-gray-900 text-gray-100 rounded-md p-3 text-xs overflow-x-auto"><code>{{ embedSnippet }}</code></pre>
          <button class="absolute top-2 right-2 text-xs bg-white/10 text-white rounded px-2 py-1 hover:bg-white/20" @click="copy(embedSnippet)">Copy</button>
        </div>
        <span class="text-xs text-gray-400">The widget bundle is served by Tring at <code>/sdk/voice.js</code> — no CDN needed.</span>
      </div>

      <div class="flex items-center gap-3">
        <button :disabled="guestSaving" class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50" @click="saveGuest">
          {{ guestSaving ? "Saving…" : "Save guest config" }}
        </button>
        <span v-if="guestSaved" class="text-sm text-green-600">Saved</span>
      </div>
    </div>
  </div>
</template>

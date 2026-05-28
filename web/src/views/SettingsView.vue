<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { Tenant } from "../api/types";

const tenant = ref<Tenant | null>(null);
const loading = ref(true);
const saving = ref(false);
const error = ref("");
const saved = ref(false);

const strategy = ref("failover");
const strategies = ["failover", "round_robin", "manual", "load_based"];

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
  </div>
</template>

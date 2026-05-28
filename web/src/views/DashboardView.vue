<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { Tenant, Call, Agent, PhoneNumber } from "../api/types";

const tenant = ref<Tenant | null>(null);
const calls = ref<Call[]>([]);
const agents = ref<Agent[]>([]);
const numbers = ref<PhoneNumber[]>([]);
const loading = ref(true);
const error = ref("");

onMounted(async () => {
  try {
    const [t, c, a, n] = await Promise.all([
      api.get<Tenant>("/v1/tenants/me"),
      api.get<Call[]>("/v1/calls?limit=8"),
      api.get<Agent[]>("/v1/agents"),
      api.get<PhoneNumber[]>("/v1/numbers"),
    ]);
    tenant.value = t;
    calls.value = c;
    agents.value = a;
    numbers.value = n;
  } catch (e) {
    error.value = e instanceof Error ? e.message : "failed to load";
  } finally {
    loading.value = false;
  }
});

const activeOutbound = () =>
  numbers.value.filter((n) => n.type === "outbound" && n.active).length;
</script>

<template>
  <div class="space-y-6">
    <h1 class="text-2xl font-semibold">Dashboard</h1>
    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
    <div v-if="loading" class="text-gray-400 text-sm">Loading…</div>

    <template v-else>
      <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
        <div class="bg-white rounded-xl border p-5">
          <div class="text-3xl font-bold">{{ agents.length }}</div>
          <div class="text-sm text-gray-500">Agents</div>
        </div>
        <div class="bg-white rounded-xl border p-5">
          <div class="text-3xl font-bold">{{ numbers.length }}</div>
          <div class="text-sm text-gray-500">Numbers</div>
        </div>
        <div class="bg-white rounded-xl border p-5">
          <div class="text-3xl font-bold">{{ activeOutbound() }}</div>
          <div class="text-sm text-gray-500">Active outbound trunks</div>
        </div>
        <div class="bg-white rounded-xl border p-5">
          <div class="text-3xl font-bold capitalize">
            {{ tenant?.outbound_strategy || "—" }}
          </div>
          <div class="text-sm text-gray-500">Trunk strategy</div>
        </div>
      </div>

      <div class="bg-white rounded-xl border">
        <div class="px-5 py-3 border-b font-medium">Recent calls</div>
        <table class="w-full text-sm">
          <thead class="text-left text-gray-500">
            <tr>
              <th class="px-5 py-2 font-medium">From</th>
              <th class="px-5 py-2 font-medium">To</th>
              <th class="px-5 py-2 font-medium">Status</th>
              <th class="px-5 py-2 font-medium">Started</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in calls" :key="c.id" class="border-t">
              <td class="px-5 py-2">{{ c.from_number }}</td>
              <td class="px-5 py-2">{{ c.to_number }}</td>
              <td class="px-5 py-2">{{ c.status }}</td>
              <td class="px-5 py-2 text-gray-500">
                {{ new Date(c.started_at).toLocaleString() }}
              </td>
            </tr>
            <tr v-if="calls.length === 0">
              <td colspan="4" class="px-5 py-6 text-center text-gray-400">No calls yet</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

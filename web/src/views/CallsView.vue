<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { Call } from "../api/types";

const calls = ref<Call[]>([]);
const loading = ref(true);
const error = ref("");
const statusFilter = ref("");

const showConnect = ref(false);
const placing = ref(false);
const connectForm = ref({ agent_number: "", client_number: "" });
const connectMsg = ref("");

const statuses = ["", "ringing", "answered", "ended"];

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const q = statusFilter.value ? `?status=${statusFilter.value}&limit=100` : "?limit=100";
    calls.value = await api.get<Call[]>(`/v1/calls${q}`);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "failed to load";
  } finally {
    loading.value = false;
  }
}

async function connect() {
  placing.value = true;
  connectMsg.value = "";
  error.value = "";
  try {
    const c = await api.post<Call>("/v1/calls/connect", connectForm.value);
    connectMsg.value = `Call placed — ${c.from_number} → ${c.to_number} (${c.status})`;
    connectForm.value = { agent_number: "", client_number: "" };
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "connect failed";
  } finally {
    placing.value = false;
  }
}

function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "—";
}

onMounted(load);
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">Calls</h1>
      <button
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
        @click="showConnect = !showConnect"
      >
        {{ showConnect ? "Cancel" : "Connect two numbers" }}
      </button>
    </div>

    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="connectMsg" class="text-sm text-green-600">{{ connectMsg }}</p>

    <form
      v-if="showConnect"
      class="bg-white rounded-xl border p-5 grid sm:grid-cols-3 gap-4 items-end"
      @submit.prevent="connect"
    >
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Agent number</span>
        <input v-model="connectForm.agent_number" required class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm" placeholder="+919876543210" />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Client number</span>
        <input v-model="connectForm.client_number" required class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm" placeholder="+919812345678" />
      </label>
      <button type="submit" :disabled="placing" class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50">
        {{ placing ? "Placing…" : "Place call" }}
      </button>
    </form>

    <div class="flex items-center gap-2">
      <span class="text-sm text-gray-500">Status:</span>
      <select v-model="statusFilter" class="rounded-md border border-gray-300 px-2 py-1 text-sm" @change="load">
        <option v-for="s in statuses" :key="s" :value="s">{{ s || "all" }}</option>
      </select>
    </div>

    <div class="bg-white rounded-xl border overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">From</th>
            <th class="px-5 py-2 font-medium">To</th>
            <th class="px-5 py-2 font-medium">Dir</th>
            <th class="px-5 py-2 font-medium">Status</th>
            <th class="px-5 py-2 font-medium">Started</th>
            <th class="px-5 py-2 font-medium">Duration</th>
            <th class="px-5 py-2 font-medium">Recording</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in calls" :key="c.id" class="border-t">
            <td class="px-5 py-2 font-mono">{{ c.from_number }}</td>
            <td class="px-5 py-2 font-mono">{{ c.to_number }}</td>
            <td class="px-5 py-2">{{ c.direction }}</td>
            <td class="px-5 py-2">{{ c.status }}</td>
            <td class="px-5 py-2 text-gray-500">{{ fmt(c.started_at) }}</td>
            <td class="px-5 py-2">{{ c.duration_seconds != null ? c.duration_seconds + "s" : "—" }}</td>
            <td class="px-5 py-2">
              <a v-if="c.recording_url" :href="c.recording_url" target="_blank" class="text-gray-600 hover:text-gray-900 underline">play</a>
              <span v-else class="text-gray-300">—</span>
            </td>
          </tr>
          <tr v-if="!loading && calls.length === 0">
            <td colspan="7" class="px-5 py-6 text-center text-gray-400">No calls</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { Call, IvrStep } from "../api/types";

const calls = ref<Call[]>([]);
const loading = ref(true);
const error = ref("");
const statusFilter = ref("");

// Per-call expand state for the IVR-path detail row.
const expanded = ref<Set<string>>(new Set());
function toggle(id: string) {
  const s = new Set(expanded.value);
  s.has(id) ? s.delete(id) : s.add(id);
  expanded.value = s;
}
function hasPath(c: Call): boolean {
  return Array.isArray(c.ivr_path) && c.ivr_path.length > 0;
}

// stepText turns one recorded step into a human-readable line, e.g.
// "Menu — pressed 1", "Transfer → alice (no answer)".
function stepText(s: IvrStep): string {
  switch (s.node_type) {
    case "menu":
      if (s.outcome.startsWith("digit:")) return `Menu — pressed ${s.outcome.slice(6)}`;
      if (s.outcome === "timeout") return "Menu — no input (timeout)";
      if (s.outcome === "max_retries")
        return `Menu — invalid input${s.dtmf_input ? ` (${s.dtmf_input})` : ""}`;
      return "Menu";
    case "transfer":
      return `Transfer → ${s.detail || "agent"} (${s.outcome === "completed" ? "answered" : "no answer"})`;
    case "greeting":
      return s.action === "speak" ? `Greeting — “${s.detail}”` : `Greeting — ${s.detail || "audio"}`;
    case "hangup":
      return "Hangup";
    case "start":
      return "Start";
    default:
      return s.outcome ? `${s.node_type} — ${s.outcome}` : s.node_type;
  }
}

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
            <th class="px-5 py-2 font-medium">Agent</th>
            <th class="px-5 py-2 font-medium">Dir</th>
            <th class="px-5 py-2 font-medium">Status</th>
            <th class="px-5 py-2 font-medium">Started</th>
            <th class="px-5 py-2 font-medium">Duration</th>
            <th class="px-5 py-2 font-medium">Recording</th>
            <th class="px-5 py-2 font-medium">IVR path</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="c in calls" :key="c.id">
            <tr class="border-t" :class="hasPath(c) ? 'cursor-pointer hover:bg-gray-50' : ''" @click="hasPath(c) && toggle(c.id)">
              <td class="px-5 py-2 font-mono"><div class="max-w-[220px] truncate" :title="c.client_ref || c.from_number">{{ c.client_ref || c.from_number }}</div></td>
              <td class="px-5 py-2 font-mono"><div class="max-w-[220px] truncate" :title="c.to_number">{{ c.to_number }}</div></td>
              <td class="px-5 py-2">{{ c.agent_name || "—" }}</td>
              <td class="px-5 py-2">{{ c.direction }}</td>
              <td class="px-5 py-2">{{ c.status }}</td>
              <td class="px-5 py-2 text-gray-500">{{ fmt(c.started_at) }}</td>
              <td class="px-5 py-2">{{ c.duration_seconds != null ? c.duration_seconds + "s" : "—" }}</td>
              <td class="px-5 py-2">
                <a v-if="c.recording_url" :href="c.recording_url" target="_blank" class="text-gray-600 hover:text-gray-900 underline" @click.stop>play</a>
                <span v-else class="text-gray-300">—</span>
              </td>
              <td class="px-5 py-2">
                <button v-if="hasPath(c)" class="text-gray-600 hover:text-gray-900" @click.stop="toggle(c.id)">
                  {{ expanded.has(c.id) ? "▾ hide" : `▸ view (${c.ivr_path!.length})` }}
                </button>
                <span v-else class="text-gray-300">—</span>
              </td>
            </tr>
            <tr v-if="hasPath(c) && expanded.has(c.id)" class="bg-gray-50 border-t">
              <td colspan="9" class="px-8 py-3">
                <ol class="space-y-1">
                  <li v-for="(step, i) in c.ivr_path" :key="i" class="flex items-center gap-3 text-sm">
                    <span class="text-gray-400 tabular-nums w-5 text-right">{{ i + 1 }}.</span>
                    <span class="text-gray-800">{{ stepText(step) }}</span>
                  </li>
                </ol>
              </td>
            </tr>
          </template>
          <tr v-if="!loading && calls.length === 0">
            <td colspan="9" class="px-5 py-6 text-center text-gray-400">No calls</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

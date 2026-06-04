<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { ApiKey } from "../api/types";

const keys = ref<ApiKey[]>([]);
const loading = ref(true);
const error = ref("");

const showForm = ref(false);
const creating = ref(false);
const newName = ref("");
const newKey = ref(""); // plaintext, shown once after create

async function load() {
  loading.value = true;
  try {
    keys.value = await api.get<ApiKey[]>("/v1/api-keys");
  } catch (e) {
    error.value = e instanceof Error ? e.message : "failed to load";
  } finally {
    loading.value = false;
  }
}

async function create() {
  creating.value = true;
  error.value = "";
  newKey.value = "";
  try {
    const k = await api.post<ApiKey>("/v1/api-keys", { name: newName.value });
    if (k.key) newKey.value = k.key;
    newName.value = "";
    showForm.value = false;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "create failed";
  } finally {
    creating.value = false;
  }
}

async function revoke(k: ApiKey) {
  if (!confirm(`Revoke "${k.name}" (${k.key_prefix}…)? Anything using it will stop working.`)) return;
  error.value = "";
  try {
    await api.del(`/v1/api-keys/${k.id}`);
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "revoke failed";
  }
}

function copy(text: string) {
  navigator.clipboard?.writeText(text);
}

function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "—";
}

onMounted(load);
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">API keys</h1>
      <button
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
        @click="showForm = !showForm"
      >
        {{ showForm ? "Cancel" : "Generate key" }}
      </button>
    </div>

    <p class="text-sm text-gray-500">
      Server-to-server credentials (<code>tk_live_…</code>) for your backend —
      e.g. minting calling credentials. Send as
      <code>Authorization: Bearer &lt;key&gt;</code>. Never expose a key in a browser.
    </p>

    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>

    <!-- Plaintext shown exactly once -->
    <div v-if="newKey" class="bg-amber-50 border border-amber-200 rounded-xl p-4 text-sm">
      <div class="font-medium">New API key — copy it now, it won't be shown again</div>
      <div class="mt-2 flex items-center gap-2">
        <code class="flex-1 break-all text-xs bg-white border rounded-md px-3 py-2">{{ newKey }}</code>
        <button class="text-xs border rounded-md px-2 py-2 hover:bg-gray-50" @click="copy(newKey)">Copy</button>
      </div>
    </div>

    <form v-if="showForm" class="bg-white rounded-xl border p-5 flex items-end gap-3" @submit.prevent="create">
      <label class="flex-1">
        <span class="text-sm font-medium text-gray-700">Name</span>
        <input v-model="newName" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm" placeholder="e.g. support-portal backend" />
        <span class="text-xs text-gray-400">A label to recognise where this key is used.</span>
      </label>
      <button type="submit" :disabled="creating" class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50">
        {{ creating ? "Generating…" : "Generate" }}
      </button>
    </form>

    <div class="bg-white rounded-xl border overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">Name</th>
            <th class="px-5 py-2 font-medium">Key</th>
            <th class="px-5 py-2 font-medium">Created</th>
            <th class="px-5 py-2 font-medium">Last used</th>
            <th class="px-5 py-2 font-medium">Status</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="k in keys" :key="k.id" class="border-t" :class="k.revoked ? 'opacity-50' : ''">
            <td class="px-5 py-2">{{ k.name }}</td>
            <td class="px-5 py-2 font-mono text-xs">{{ k.key_prefix }}…</td>
            <td class="px-5 py-2 text-gray-500">{{ fmt(k.created_at) }}</td>
            <td class="px-5 py-2 text-gray-500">{{ fmt(k.last_used_at) }}</td>
            <td class="px-5 py-2">
              <span v-if="k.revoked" class="text-gray-400">revoked</span>
              <span v-else class="text-green-700">active</span>
            </td>
            <td class="px-5 py-2 text-right">
              <button v-if="!k.revoked" class="text-red-600 hover:text-red-800" @click="revoke(k)">Revoke</button>
            </td>
          </tr>
          <tr v-if="!loading && keys.length === 0">
            <td colspan="6" class="px-5 py-6 text-center text-gray-400">No API keys yet</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

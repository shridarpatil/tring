<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { Agent, AgentToken } from "../api/types";

const agents = ref<Agent[]>([]);
const loading = ref(true);
const error = ref("");

const showForm = ref(false);
const form = ref({ sip_username: "", display_name: "" });
const saving = ref(false);

const tokenFor = ref<string>("");
const token = ref<AgentToken | null>(null);

async function load() {
  loading.value = true;
  try {
    agents.value = await api.get<Agent[]>("/v1/agents");
  } catch (e) {
    error.value = e instanceof Error ? e.message : "failed to load";
  } finally {
    loading.value = false;
  }
}

async function create() {
  saving.value = true;
  error.value = "";
  try {
    await api.post<Agent>("/v1/agents", form.value);
    form.value = { sip_username: "", display_name: "" };
    showForm.value = false;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "create failed";
  } finally {
    saving.value = false;
  }
}

async function mintToken(a: Agent) {
  tokenFor.value = a.sip_username;
  token.value = null;
  try {
    token.value = await api.get<AgentToken>(`/v1/agents/${a.id}/token`);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "token failed";
  }
}

onMounted(load);
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">Agents</h1>
      <button
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
        @click="showForm = !showForm"
      >
        {{ showForm ? "Cancel" : "New agent" }}
      </button>
    </div>

    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>

    <form
      v-if="showForm"
      class="bg-white rounded-xl border p-5 grid sm:grid-cols-3 gap-4 items-end"
      @submit.prevent="create"
    >
      <label class="block">
        <span class="text-sm font-medium text-gray-700">SIP username</span>
        <input
          v-model="form.sip_username"
          required
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          placeholder="alice"
        />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Display name</span>
        <input
          v-model="form.display_name"
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          placeholder="Alice"
        />
      </label>
      <button
        type="submit"
        :disabled="saving"
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50"
      >
        {{ saving ? "Creating…" : "Create" }}
      </button>
    </form>

    <div v-if="token" class="bg-amber-50 border border-amber-200 rounded-xl p-4 text-sm">
      <div class="font-medium">Token for {{ tokenFor }} (expires {{ new Date(token.expires_at).toLocaleTimeString() }})</div>
      <code class="block mt-1 break-all text-xs">{{ token.token }}</code>
    </div>

    <div class="bg-white rounded-xl border">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">SIP username</th>
            <th class="px-5 py-2 font-medium">Display name</th>
            <th class="px-5 py-2 font-medium">Created</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in agents" :key="a.id" class="border-t">
            <td class="px-5 py-2 font-mono">{{ a.sip_username }}</td>
            <td class="px-5 py-2">{{ a.display_name }}</td>
            <td class="px-5 py-2 text-gray-500">{{ new Date(a.created_at).toLocaleDateString() }}</td>
            <td class="px-5 py-2 text-right">
              <button class="text-gray-600 hover:text-gray-900" @click="mintToken(a)">Get token</button>
            </td>
          </tr>
          <tr v-if="!loading && agents.length === 0">
            <td colspan="4" class="px-5 py-6 text-center text-gray-400">No agents yet</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

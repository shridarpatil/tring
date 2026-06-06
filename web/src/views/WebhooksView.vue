<script setup lang="ts">
import { ref } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { api } from "../api/client";
import type { Webhook } from "../api/types";

const { items: webhooks, loading, error, load } = useList<Webhook>("/v1/webhooks");

const showForm = ref(false);
const saving = ref(false);
const form = ref({ url: "", events: [] as string[] });
const newSecret = ref("");

const eventTypes = ["call.ringing", "call.answered", "call.ended"];

async function create() {
  saving.value = true;
  error.value = "";
  newSecret.value = "";
  try {
    const w = await api.post<Webhook>("/v1/webhooks", form.value);
    if (w.secret) newSecret.value = w.secret;
    form.value = { url: "", events: [] };
    showForm.value = false;
    await load();
  } catch (e) {
    error.value = errMessage(e, "create failed");
  } finally {
    saving.value = false;
  }
}

async function remove(w: Webhook) {
  if (!confirm(`Delete webhook ${w.url}?`)) return;
  error.value = "";
  try {
    await api.del(`/v1/webhooks/${w.id}`);
    await load();
  } catch (e) {
    error.value = errMessage(e, "delete failed");
  }
}
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">Webhooks</h1>
      <button
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
        @click="showForm = !showForm"
      >
        {{ showForm ? "Cancel" : "Add webhook" }}
      </button>
    </div>

    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>

    <div v-if="newSecret" class="bg-amber-50 border border-amber-200 rounded-xl p-4 text-sm">
      <div class="font-medium">Signing secret (shown once — save it)</div>
      <code class="block mt-1 break-all text-xs">{{ newSecret }}</code>
    </div>

    <form v-if="showForm" class="bg-white rounded-xl border p-5 space-y-4" @submit.prevent="create">
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Delivery URL</span>
        <input v-model="form.url" type="url" required class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm" placeholder="https://your-crm.com/tring/webhook" />
      </label>
      <div>
        <span class="text-sm font-medium text-gray-700">Events</span>
        <div class="mt-2 flex flex-wrap gap-3">
          <label v-for="ev in eventTypes" :key="ev" class="flex items-center gap-2 text-sm">
            <input type="checkbox" :value="ev" v-model="form.events" />
            {{ ev }}
          </label>
        </div>
        <p class="text-xs text-gray-400 mt-1">Leave all unchecked to receive every event.</p>
      </div>
      <button type="submit" :disabled="saving" class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50">
        {{ saving ? "Saving…" : "Create" }}
      </button>
    </form>

    <div class="bg-white rounded-xl border">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">URL</th>
            <th class="px-5 py-2 font-medium">Events</th>
            <th class="px-5 py-2 font-medium">Created</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="w in webhooks" :key="w.id" class="border-t">
            <td class="px-5 py-2 font-mono text-xs break-all">{{ w.url }}</td>
            <td class="px-5 py-2">{{ w.events.length ? w.events.join(", ") : "all" }}</td>
            <td class="px-5 py-2 text-gray-500">{{ new Date(w.created_at).toLocaleDateString() }}</td>
            <td class="px-5 py-2 text-right">
              <button class="text-red-600 hover:text-red-800" @click="remove(w)">Delete</button>
            </td>
          </tr>
          <tr v-if="!loading && webhooks.length === 0">
            <td colspan="4" class="px-5 py-6 text-center text-gray-400">No webhooks</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

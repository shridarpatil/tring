<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { PhoneNumber } from "../api/types";

const numbers = ref<PhoneNumber[]>([]);
const loading = ref(true);
const error = ref("");

const showForm = ref(false);
const saving = ref(false);
const form = ref({ e164: "", type: "outbound", provider: "tata", priority: 100 });

const providers = ["tata", "exotel", "airtel", "jio", "loopback"];
const types = ["outbound", "inbound", "masking"];

async function load() {
  loading.value = true;
  try {
    numbers.value = await api.get<PhoneNumber[]>("/v1/numbers");
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
    await api.post("/v1/numbers", {
      e164: form.value.e164,
      type: form.value.type,
      provider: form.value.provider,
      priority: form.value.priority,
    });
    form.value = { e164: "", type: "outbound", provider: "tata", priority: 100 };
    showForm.value = false;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "create failed";
  } finally {
    saving.value = false;
  }
}

async function toggleActive(n: PhoneNumber) {
  error.value = "";
  try {
    await api.patch(`/v1/numbers/${n.id}`, { active: !n.active });
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "update failed";
  }
}

onMounted(load);
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">Numbers</h1>
      <button
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
        @click="showForm = !showForm"
      >
        {{ showForm ? "Cancel" : "Register number" }}
      </button>
    </div>

    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>

    <form
      v-if="showForm"
      class="bg-white rounded-xl border p-5 grid sm:grid-cols-5 gap-4 items-end"
      @submit.prevent="create"
    >
      <label class="block">
        <span class="text-sm font-medium text-gray-700">E.164</span>
        <input v-model="form.e164" required class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm" placeholder="+918001234567" />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Type</span>
        <select v-model="form.type" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option v-for="t in types" :key="t" :value="t">{{ t }}</option>
        </select>
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Provider</span>
        <select v-model="form.provider" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option v-for="p in providers" :key="p" :value="p">{{ p }}</option>
        </select>
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Priority</span>
        <input v-model.number="form.priority" type="number" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm" />
      </label>
      <button type="submit" :disabled="saving" class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50">
        {{ saving ? "Saving…" : "Register" }}
      </button>
    </form>

    <div class="bg-white rounded-xl border">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">Number</th>
            <th class="px-5 py-2 font-medium">Type</th>
            <th class="px-5 py-2 font-medium">Provider</th>
            <th class="px-5 py-2 font-medium">Priority</th>
            <th class="px-5 py-2 font-medium">Active</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="n in numbers" :key="n.id" class="border-t">
            <td class="px-5 py-2 font-mono">{{ n.e164 }}</td>
            <td class="px-5 py-2">{{ n.type }}</td>
            <td class="px-5 py-2">{{ n.provider }}</td>
            <td class="px-5 py-2">{{ n.priority }}</td>
            <td class="px-5 py-2">
              <span :class="n.active ? 'text-green-600' : 'text-gray-400'">{{ n.active ? "yes" : "no" }}</span>
            </td>
            <td class="px-5 py-2 text-right">
              <button class="text-gray-600 hover:text-gray-900" @click="toggleActive(n)">
                {{ n.active ? "Deactivate" : "Activate" }}
              </button>
            </td>
          </tr>
          <tr v-if="!loading && numbers.length === 0">
            <td colspan="6" class="px-5 py-6 text-center text-gray-400">No numbers registered</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

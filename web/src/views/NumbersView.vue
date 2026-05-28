<script setup lang="ts">
// NumbersView — register DIDs and bind them to a trunk. Outbound numbers
// MUST reference a trunk (where the call lands); inbound/masking numbers
// can stand alone.
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import type { PhoneNumber, Trunk } from "../api/types";

const numbers = ref<PhoneNumber[]>([]);
const trunks = ref<Trunk[]>([]);
const loading = ref(true);
const error = ref("");

const showForm = ref(false);
const saving = ref(false);
const form = ref<{
  e164: string;
  type: "inbound" | "outbound" | "masking";
  trunk_id: string;
  priority: number;
}>({ e164: "", type: "outbound", trunk_id: "", priority: 100 });

const types = ["outbound", "inbound", "masking"];

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const [n, t] = await Promise.all([
      api.get<PhoneNumber[]>("/v1/numbers"),
      api.get<Trunk[]>("/v1/trunks"),
    ]);
    numbers.value = n;
    trunks.value = t;
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
    const body: Record<string, unknown> = {
      e164: form.value.e164,
      type: form.value.type,
      priority: form.value.priority,
    };
    if (form.value.trunk_id) body.trunk_id = form.value.trunk_id;
    await api.post("/v1/numbers", body);
    form.value = { e164: "", type: "outbound", trunk_id: "", priority: 100 };
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

function trunkName(id: string | null): string {
  if (!id) return "—";
  const t = trunks.value.find((x) => x.id === id);
  return t ? t.name : id.slice(0, 8);
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
      class="bg-white rounded-xl border border-gray-200 p-5 grid sm:grid-cols-5 gap-4 items-end"
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
        <span class="text-sm font-medium text-gray-700">
          Trunk<span v-if="form.type === 'outbound'" class="text-red-500">*</span>
        </span>
        <select v-model="form.trunk_id" :required="form.type === 'outbound'" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option value="">— none —</option>
          <option v-for="t in trunks.filter((x) => x.active)" :key="t.id" :value="t.id">
            {{ t.name }}{{ t.status ? ` (${t.status})` : '' }}
          </option>
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

    <div class="bg-white rounded-xl border border-gray-200">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">Number</th>
            <th class="px-5 py-2 font-medium">Type</th>
            <th class="px-5 py-2 font-medium">Trunk</th>
            <th class="px-5 py-2 font-medium">Priority</th>
            <th class="px-5 py-2 font-medium">Active</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="n in numbers" :key="n.id" class="border-t border-gray-200">
            <td class="px-5 py-2 font-mono">{{ n.e164 }}</td>
            <td class="px-5 py-2">{{ n.type }}</td>
            <td class="px-5 py-2 text-xs">{{ trunkName(n.trunk_id) }}</td>
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

<script setup lang="ts">
// FlowsListView — list/create/delete IVR flows via the Tring API.
import { ref, onMounted } from "vue";
import { useRouter } from "vue-router";
import { Plus, Trash2, ArrowRight } from "lucide-vue-next";
import { api } from "../../api/client";
import type { Flow } from "../../api/types";

const router = useRouter();

const flows = ref<Flow[]>([]);
const showCreate = ref(false);
const newName = ref("");
const loading = ref(true);
const error = ref("");

async function refresh() {
  loading.value = true;
  error.value = "";
  try {
    flows.value = await api.get<Flow[]>("/v1/flows");
  } catch (e) {
    error.value = e instanceof Error ? e.message : "failed to load";
  } finally {
    loading.value = false;
  }
}

async function createFlow() {
  if (!newName.value.trim()) return;
  error.value = "";
  try {
    const f = await api.post<Flow>("/v1/flows", { name: newName.value.trim() });
    router.push({ name: "flow-editor", params: { id: f.id } });
  } catch (e) {
    error.value = e instanceof Error ? e.message : "create failed";
  }
}

async function deleteFlow(id: string) {
  if (!confirm("Delete this flow?")) return;
  error.value = "";
  try {
    await api.del(`/v1/flows/${id}`);
    await refresh();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "delete failed";
  }
}

onMounted(refresh);
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">IVR Flows</h1>
      <button
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 flex items-center gap-1.5"
        @click="showCreate = !showCreate"
      >
        <Plus class="h-4 w-4" />
        {{ showCreate ? "Cancel" : "New flow" }}
      </button>
    </div>

    <form
      v-if="showCreate"
      class="bg-white rounded-xl border border-gray-200 p-5 grid sm:grid-cols-[1fr_auto] gap-3 items-end"
      @submit.prevent="createFlow"
    >
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Flow name</span>
        <input
          v-model="newName"
          required
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          placeholder="Main IVR"
        />
      </label>
      <button
        type="submit"
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
      >
        Create &amp; edit
      </button>
    </form>

    <div class="bg-white rounded-xl border border-gray-200">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">Name</th>
            <th class="px-5 py-2 font-medium">Active</th>
            <th class="px-5 py-2 font-medium">Call start</th>
            <th class="px-5 py-2 font-medium">Nodes</th>
            <th class="px-5 py-2 font-medium">Updated</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="f in flows" :key="f.id" class="border-t border-gray-200 hover:bg-gray-50">
            <td class="px-5 py-2 font-medium">{{ f.name }}</td>
            <td class="px-5 py-2">
              <span :class="f.is_active ? 'text-green-600' : 'text-gray-400'">
                {{ f.is_active ? "yes" : "no" }}
              </span>
            </td>
            <td class="px-5 py-2">
              <span :class="f.is_call_start ? 'text-green-600' : 'text-gray-400'">
                {{ f.is_call_start ? "yes" : "—" }}
              </span>
            </td>
            <td class="px-5 py-2 text-gray-500">{{ f.definition?.nodes?.length || 0 }}</td>
            <td class="px-5 py-2 text-gray-500">
              {{ new Date(f.updated_at).toLocaleString() }}
            </td>
            <td class="px-5 py-2 text-right">
              <button
                class="text-gray-600 hover:text-gray-900 mr-3"
                @click="router.push({ name: 'flow-editor', params: { id: f.id } })"
              >
                <span class="inline-flex items-center gap-1">Edit <ArrowRight class="h-3 w-3" /></span>
              </button>
              <button class="text-red-600 hover:text-red-800" @click="deleteFlow(f.id)">
                <Trash2 class="h-4 w-4 inline" />
              </button>
            </td>
          </tr>
          <tr v-if="flows.length === 0">
            <td colspan="6" class="px-5 py-8 text-center text-gray-400">
              No flows yet — click <span class="font-medium">New flow</span> to start.
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
    <p class="text-xs text-gray-400">
      Flows persist via the API. The FreeSWITCH executor (Phase 3) is
      not wired yet — saving doesn't yet make inbound calls run the
      flow; the editor is fully functional for design.
    </p>
  </div>
</template>

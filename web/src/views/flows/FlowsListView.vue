<script setup lang="ts">
// FlowsListView — list/create/delete IVR flows. localStorage stub until
// the backend lands.
import { ref, onMounted } from "vue";
import { useRouter } from "vue-router";
import { Plus, Trash2, ArrowRight } from "lucide-vue-next";
import type { Flow, IVRFlowData } from "../../api/types";

const router = useRouter();
const STORAGE_KEY = "tring_flows_v1";

const flows = ref<Flow[]>([]);
const showCreate = ref(false);
const newName = ref("");

function loadAll(): Record<string, Flow> {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}");
  } catch {
    return {};
  }
}
function saveAll(all: Record<string, Flow>) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(all));
}
function refresh() {
  flows.value = Object.values(loadAll()).sort((a, b) => b.updated_at.localeCompare(a.updated_at));
}

function emptyDef(): IVRFlowData {
  return { version: 2, nodes: [], edges: [], entry_node: "" };
}

function createFlow() {
  if (!newName.value.trim()) return;
  const id = `flow_${Date.now()}`;
  const now = new Date().toISOString();
  const all = loadAll();
  all[id] = {
    id,
    tenant_id: "local",
    name: newName.value.trim(),
    is_active: true,
    is_call_start: false,
    is_outgoing_end: false,
    definition: emptyDef(),
    created_at: now,
    updated_at: now,
  };
  saveAll(all);
  router.push({ name: "flow-editor", params: { id } });
}

function deleteFlow(id: string) {
  if (!confirm("Delete this flow?")) return;
  const all = loadAll();
  delete all[id];
  saveAll(all);
  refresh();
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

    <p class="text-xs text-gray-400">
      Flows are saved to your browser (localStorage) until the backend
      endpoint lands. They won't sync to the server or to FreeSWITCH yet.
    </p>
  </div>
</template>

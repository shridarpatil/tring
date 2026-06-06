<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { api } from "../api/client";
import type { Agent, Team, TeamStrategy } from "../api/types";

const teams = ref<Team[]>([]);
const agents = ref<Agent[]>([]);
const loading = ref(true);
const error = ref("");

const strategies: { value: TeamStrategy; label: string; hint: string }[] = [
  { value: "ring_all", label: "Ring all", hint: "Ring every available member at once" },
  { value: "round_robin", label: "Round robin", hint: "Rotate who rings first (spread load)" },
  { value: "sequential", label: "Sequential", hint: "Ring members one at a time, in order" },
  { value: "longest_wait", label: "Longest idle", hint: "Ring the member idle the longest" },
];

const showForm = ref(false);
const form = ref<{ name: string; strategy: TeamStrategy; ring_seconds: number; max_wait_seconds: number }>({
  name: "",
  strategy: "ring_all",
  ring_seconds: 20,
  max_wait_seconds: 120,
});
const saving = ref(false);

// The currently-expanded team (detail panel with members).
const selected = ref<Team | null>(null);
const addAgentId = ref("");
const addPosition = ref(1);

const nonMembers = computed(() => {
  const inTeam = new Set((selected.value?.members ?? []).map((m) => m.agent_id));
  return agents.value.filter((a) => !inTeam.has(a.id));
});

async function load() {
  loading.value = true;
  try {
    [teams.value, agents.value] = await Promise.all([
      api.get<Team[]>("/v1/teams"),
      api.get<Agent[]>("/v1/agents"),
    ]);
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
    await api.post<Team>("/v1/teams", form.value);
    form.value = { name: "", strategy: "ring_all", ring_seconds: 20, max_wait_seconds: 120 };
    showForm.value = false;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "create failed";
  } finally {
    saving.value = false;
  }
}

async function open(t: Team) {
  if (selected.value?.name === t.name) {
    selected.value = null;
    return;
  }
  try {
    selected.value = await api.get<Team>(`/v1/teams/${encodeURIComponent(t.name)}`);
    addAgentId.value = "";
    addPosition.value = (selected.value.members?.length ?? 0) + 1;
  } catch (e) {
    error.value = e instanceof Error ? e.message : "load team failed";
  }
}

async function saveSettings() {
  if (!selected.value) return;
  error.value = "";
  try {
    const { strategy, ring_seconds, max_wait_seconds } = selected.value;
    const updated = await api.patch<Team>(`/v1/teams/${encodeURIComponent(selected.value.name)}`, {
      strategy,
      ring_seconds,
      max_wait_seconds,
    });
    // Keep members (PATCH doesn't return them) and refresh the list row.
    selected.value = { ...updated, members: selected.value.members };
    await refreshList();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "save failed";
  }
}

async function addMember() {
  if (!selected.value || !addAgentId.value) return;
  error.value = "";
  try {
    const members = await api.post<Team["members"]>(
      `/v1/teams/${encodeURIComponent(selected.value.name)}/agents`,
      { agent_id: addAgentId.value, position: addPosition.value },
    );
    selected.value = { ...selected.value, members: members ?? [] };
    addAgentId.value = "";
    addPosition.value = (selected.value.members?.length ?? 0) + 1;
  } catch (e) {
    error.value = e instanceof Error ? e.message : "add failed";
  }
}

async function removeMember(agentId: string) {
  if (!selected.value) return;
  error.value = "";
  try {
    await api.del<void>(`/v1/teams/${encodeURIComponent(selected.value.name)}/agents/${agentId}`);
    selected.value = {
      ...selected.value,
      members: (selected.value.members ?? []).filter((m) => m.agent_id !== agentId),
    };
  } catch (e) {
    error.value = e instanceof Error ? e.message : "remove failed";
  }
}

async function destroy(t: Team) {
  if (!confirm(`Delete team "${t.name}"? Flows referencing it will fall back to no-answer.`)) return;
  error.value = "";
  try {
    await api.del<void>(`/v1/teams/${encodeURIComponent(t.name)}`);
    if (selected.value?.name === t.name) selected.value = null;
    await refreshList();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "delete failed";
  }
}

async function refreshList() {
  teams.value = await api.get<Team[]>("/v1/teams");
}

function strategyLabel(s: string) {
  return strategies.find((x) => x.value === s)?.label ?? s;
}

onMounted(load);
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">Teams</h1>
      <button
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
        @click="showForm = !showForm"
      >
        {{ showForm ? "Cancel" : "New team" }}
      </button>
    </div>

    <p class="text-sm text-gray-500">
      A team is a group of agents the IVR <span class="font-medium">Transfer</span> node rings as a
      unit, using the team's strategy. When every member is busy the caller waits in the team's queue.
    </p>
    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>

    <form
      v-if="showForm"
      class="bg-white rounded-xl border p-5 grid sm:grid-cols-4 gap-4 items-end"
      @submit.prevent="create"
    >
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Name</span>
        <input
          v-model="form.name"
          required
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          placeholder="Sales"
        />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Strategy</span>
        <select v-model="form.strategy" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option v-for="s in strategies" :key="s.value" :value="s.value">{{ s.label }}</option>
        </select>
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Ring secs</span>
        <input
          v-model.number="form.ring_seconds"
          type="number"
          min="1"
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
        />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Max wait secs</span>
        <input
          v-model.number="form.max_wait_seconds"
          type="number"
          min="1"
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
        />
      </label>
      <button
        type="submit"
        :disabled="saving"
        class="sm:col-span-4 bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50"
      >
        {{ saving ? "Creating…" : "Create team" }}
      </button>
    </form>

    <div class="bg-white rounded-xl border">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">Name</th>
            <th class="px-5 py-2 font-medium">Strategy</th>
            <th class="px-5 py-2 font-medium">Ring</th>
            <th class="px-5 py-2 font-medium">Max wait</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="t in teams" :key="t.name">
            <tr class="border-t hover:bg-gray-50 cursor-pointer" @click="open(t)">
              <td class="px-5 py-2 font-medium">{{ t.name }}</td>
              <td class="px-5 py-2">{{ strategyLabel(t.strategy) }}</td>
              <td class="px-5 py-2 text-gray-500">{{ t.ring_seconds }}s</td>
              <td class="px-5 py-2 text-gray-500">{{ t.max_wait_seconds }}s</td>
              <td class="px-5 py-2 text-right" @click.stop>
                <button class="text-red-600 hover:text-red-800" @click="destroy(t)">Delete</button>
              </td>
            </tr>
            <tr v-if="selected && selected.name === t.name" class="border-t bg-gray-50/60">
              <td colspan="5" class="px-5 py-4">
                <div class="grid sm:grid-cols-3 gap-4 items-end mb-4">
                  <label class="block">
                    <span class="text-xs font-medium text-gray-600">Strategy</span>
                    <select
                      v-model="selected.strategy"
                      class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm bg-white"
                    >
                      <option v-for="s in strategies" :key="s.value" :value="s.value">{{ s.label }}</option>
                    </select>
                  </label>
                  <label class="block">
                    <span class="text-xs font-medium text-gray-600">Ring secs</span>
                    <input
                      v-model.number="selected.ring_seconds"
                      type="number"
                      min="1"
                      class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm bg-white"
                    />
                  </label>
                  <label class="block">
                    <span class="text-xs font-medium text-gray-600">Max wait secs</span>
                    <input
                      v-model.number="selected.max_wait_seconds"
                      type="number"
                      min="1"
                      class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm bg-white"
                    />
                  </label>
                </div>
                <button
                  class="text-sm rounded-md px-3 py-1.5 border border-gray-300 bg-white hover:bg-gray-100 mb-4"
                  @click="saveSettings"
                >
                  Save settings
                </button>

                <div class="text-xs font-medium text-gray-600 mb-1">Members (position orders sequential ring)</div>
                <table class="w-full text-sm bg-white rounded-md border">
                  <tbody>
                    <tr v-for="m in selected.members" :key="m.agent_id" class="border-t first:border-t-0">
                      <td class="px-3 py-1.5 text-gray-400 w-10">{{ m.position }}</td>
                      <td class="px-3 py-1.5 font-mono">{{ m.sip_username }}</td>
                      <td class="px-3 py-1.5">{{ m.display_name }}</td>
                      <td class="px-3 py-1.5 text-right">
                        <button class="text-red-600 hover:text-red-800" @click="removeMember(m.agent_id)">
                          Remove
                        </button>
                      </td>
                    </tr>
                    <tr v-if="!selected.members?.length">
                      <td colspan="4" class="px-3 py-3 text-center text-gray-400">No members yet</td>
                    </tr>
                  </tbody>
                </table>

                <div class="flex items-end gap-2 mt-3">
                  <label class="block flex-1">
                    <span class="text-xs font-medium text-gray-600">Add agent</span>
                    <select
                      v-model="addAgentId"
                      class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm bg-white"
                    >
                      <option value="">Select an agent…</option>
                      <option v-for="a in nonMembers" :key="a.id" :value="a.id">
                        {{ a.display_name || a.sip_username }}
                      </option>
                    </select>
                  </label>
                  <label class="block w-24">
                    <span class="text-xs font-medium text-gray-600">Position</span>
                    <input
                      v-model.number="addPosition"
                      type="number"
                      min="1"
                      class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm bg-white"
                    />
                  </label>
                  <button
                    class="text-sm rounded-md px-3 py-2 bg-gray-900 text-white hover:bg-gray-800 disabled:opacity-50"
                    :disabled="!addAgentId"
                    @click="addMember"
                  >
                    Add
                  </button>
                </div>
              </td>
            </tr>
          </template>
          <tr v-if="!loading && teams.length === 0">
            <td colspan="5" class="px-5 py-6 text-center text-gray-400">No teams yet</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

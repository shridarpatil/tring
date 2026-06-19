<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { Trash2 } from "lucide-vue-next";
import type { Agent, Team, TeamStrategy } from "../api/types";
import {
  Button,
  IconButton,
  Select,
  TextField,
  Modal,
} from "../components/ui";

const teams = ref<Team[]>([]);
const agents = ref<Agent[]>([]);
const loading = ref(true);

const strategies: { value: TeamStrategy; label: string; hint: string }[] = [
  { value: "ring_all", label: "Ring all", hint: "Ring every available member at once" },
  { value: "round_robin", label: "Round robin", hint: "Rotate who rings first (spread load)" },
  { value: "sequential", label: "Sequential", hint: "Ring members one at a time, in order" },
  { value: "longest_wait", label: "Longest idle", hint: "Ring the member idle the longest" },
];
const strategyOptions = strategies.map((s) => ({ value: s.value, label: s.label }));

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

const addAgentOptions = computed(() =>
  nonMembers.value.map((a) => ({ value: a.id, label: a.display_name || a.sip_username })),
);

async function load() {
  loading.value = true;
  try {
    [teams.value, agents.value] = await Promise.all([
      api.get<Team[]>("/v1/teams"),
      api.get<Agent[]>("/v1/agents"),
    ]);
  } catch (e) {
    toast.error(errMessage(e, "failed to load"));
  } finally {
    loading.value = false;
  }
}

async function create() {
  saving.value = true;
  try {
    await api.post<Team>("/v1/teams", form.value);
    form.value = { name: "", strategy: "ring_all", ring_seconds: 20, max_wait_seconds: 120 };
    showForm.value = false;
    await load();
    toast.success("Team created");
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
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
    toast.error(errMessage(e, "load team failed"));
  }
}

async function saveSettings() {
  if (!selected.value) return;
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
    toast.success("Settings saved");
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  }
}

async function addMember() {
  if (!selected.value || !addAgentId.value) return;
  try {
    const members = await api.post<Team["members"]>(
      `/v1/teams/${encodeURIComponent(selected.value.name)}/agents`,
      { agent_id: addAgentId.value, position: addPosition.value },
    );
    selected.value = { ...selected.value, members: members ?? [] };
    addAgentId.value = "";
    addPosition.value = (selected.value.members?.length ?? 0) + 1;
  } catch (e) {
    toast.error(errMessage(e, "add failed"));
  }
}

async function removeMember(agentId: string) {
  if (!selected.value) return;
  try {
    await api.del<void>(`/v1/teams/${encodeURIComponent(selected.value.name)}/agents/${agentId}`);
    selected.value = {
      ...selected.value,
      members: (selected.value.members ?? []).filter((m) => m.agent_id !== agentId),
    };
  } catch (e) {
    toast.error(errMessage(e, "remove failed"));
  }
}

async function destroy(t: Team) {
  if (!confirm(`Delete team "${t.name}"? Flows referencing it will fall back to no-answer.`)) return;
  try {
    await api.del<void>(`/v1/teams/${encodeURIComponent(t.name)}`);
    if (selected.value?.name === t.name) selected.value = null;
    await refreshList();
    toast.success("Team deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
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
      <h1 class="text-xl font-semibold tracking-tight">Teams</h1>
      <Button @click="showForm = true">New team</Button>
    </div>

    <p class="text-sm text-ink-muted">
      A team is a group of agents the IVR <span class="font-medium text-ink">Transfer</span> node rings as a
      unit, using the team's strategy. When every member is busy the caller waits in the team's queue.
    </p>

    <div class="rounded-xl bg-surface border border-border overflow-x-auto">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-border text-left">
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">Name</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">Strategy</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint text-right">Ring</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint text-right">Max wait</th>
            <th class="px-4 h-10 w-0"></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="t in teams" :key="t.name">
            <tr
              class="border-b border-border/60 transition-colors cursor-pointer hover:bg-surface-2/40"
              @click="open(t)"
            >
              <td class="px-4 py-2.5 font-medium text-ink">{{ t.name }}</td>
              <td class="px-4 py-2.5 text-ink">{{ strategyLabel(t.strategy) }}</td>
              <td class="px-4 py-2.5 text-right text-ink-muted">{{ t.ring_seconds }}s</td>
              <td class="px-4 py-2.5 text-right text-ink-muted">{{ t.max_wait_seconds }}s</td>
              <td class="px-4 py-2.5 text-right whitespace-nowrap" @click.stop>
                <Button variant="ghost" size="sm" @click="open(t)">
                  {{ selected && selected.name === t.name ? "Close" : "Manage" }}
                </Button>
                <IconButton title="Delete team" size="sm" variant="danger" @click="destroy(t)">
                  <Trash2 class="h-4 w-4" />
                </IconButton>
              </td>
            </tr>
            <tr
              v-if="selected && selected.name === t.name"
              class="border-b border-border bg-surface-2/40"
            >
              <td colspan="5" class="px-5 py-4">
                <div class="grid sm:grid-cols-3 gap-4 items-end mb-4">
                  <Select v-model="selected.strategy" label="Strategy" :options="strategyOptions" />
                  <TextField v-model.number="selected.ring_seconds" type="number" label="Ring secs" />
                  <TextField v-model.number="selected.max_wait_seconds" type="number" label="Max wait secs" />
                </div>
                <Button variant="secondary" size="sm" class="mb-4" @click="saveSettings">Save settings</Button>

                <div class="text-xs font-medium text-ink-muted mb-1">
                  Members (position orders sequential ring)
                </div>
                <div class="rounded-lg border border-border overflow-hidden bg-surface">
                  <table class="w-full text-sm">
                    <tbody>
                      <tr
                        v-for="m in selected.members"
                        :key="m.agent_id"
                        class="border-t border-border first:border-t-0"
                      >
                        <td class="px-3 py-1.5 text-ink-faint w-10">{{ m.position }}</td>
                        <td class="px-3 py-1.5 font-mono text-ink">{{ m.sip_username }}</td>
                        <td class="px-3 py-1.5 text-ink-muted">{{ m.display_name }}</td>
                        <td class="px-3 py-1.5 text-right">
                          <IconButton title="Remove member" size="sm" variant="danger" @click="removeMember(m.agent_id)">
                            <Trash2 class="h-4 w-4" />
                          </IconButton>
                        </td>
                      </tr>
                      <tr v-if="!selected.members?.length">
                        <td colspan="4" class="px-3 py-3 text-center text-ink-faint">No members yet</td>
                      </tr>
                    </tbody>
                  </table>
                </div>

                <div class="flex items-end gap-2 mt-3">
                  <div class="flex-1">
                    <Select
                      v-model="addAgentId"
                      label="Add agent"
                      placeholder="Select an agent…"
                      :options="addAgentOptions"
                    />
                  </div>
                  <div class="w-24">
                    <TextField v-model.number="addPosition" type="number" label="Position" />
                  </div>
                  <Button :disabled="!addAgentId" @click="addMember">Add</Button>
                </div>
              </td>
            </tr>
          </template>
          <tr v-if="!loading && teams.length === 0">
            <td colspan="5" class="px-4 py-10 text-center text-ink-faint">No teams yet</td>
          </tr>
          <tr v-if="loading">
            <td colspan="5" class="px-4 py-10 text-center text-ink-faint">Loading…</td>
          </tr>
        </tbody>
      </table>
    </div>

    <Modal v-model:open="showForm" title="New team">
      <form id="team-form" class="space-y-4" @submit.prevent="create">
        <TextField v-model="form.name" label="Name" required placeholder="Sales" />
        <Select v-model="form.strategy" label="Strategy" :options="strategyOptions" />
        <div class="grid grid-cols-2 gap-4">
          <TextField v-model.number="form.ring_seconds" type="number" label="Ring secs" />
          <TextField v-model.number="form.max_wait_seconds" type="number" label="Max wait secs" />
        </div>
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="team-form" :loading="saving">Create team</Button>
      </template>
    </Modal>
  </div>
</template>

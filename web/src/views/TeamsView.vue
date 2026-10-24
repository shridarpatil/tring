<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import type { Agent, Team, TeamStrategy } from "../api/types";
import {
  Button,
  Select,
  TextField,
  Modal,
} from "../components/ui";
import { confirmDialog } from "../lib/confirm";

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
  if (!(await confirmDialog(`Delete team "${t.name}"? Flows referencing it will fall back to no-answer.`))) return;
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
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">Teams</h1>
      <Button @click="showForm = true">New team</Button>
    </div>

    <p class="text-light">
      A team is a group of agents the IVR <span style="font-weight: 500">Transfer</span> node rings as a
      unit, using the team's strategy. When every member is busy the caller waits in the team's queue.
    </p>

    <div class="card" style="padding: 0">
      <div class="table">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Strategy</th>
              <th class="align-right">Ring</th>
              <th class="align-right">Max wait</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <template v-for="t in teams" :key="t.name">
              <tr style="cursor: pointer" @click="open(t)">
                <td style="font-weight: 500">{{ t.name }}</td>
                <td>{{ strategyLabel(t.strategy) }}</td>
                <td class="align-right text-light">{{ t.ring_seconds }}s</td>
                <td class="align-right text-light">{{ t.max_wait_seconds }}s</td>
                <td @click.stop>
                  <div class="hstack row-actions">
                    <Button variant="secondary" size="sm" @click="open(t)">
                      {{ selected && selected.name === t.name ? "Close" : "Manage" }}
                    </Button>
                    <Button variant="danger" size="sm" @click="destroy(t)">Delete</Button>
                  </div>
                </td>
              </tr>
              <tr v-if="selected && selected.name === t.name" style="background: var(--secondary)">
                <td colspan="5">
                  <div class="grid grid-3" style="align-items: end; margin-bottom: 1rem">
                    <Select v-model="selected.strategy" label="Strategy" :options="strategyOptions" />
                    <TextField v-model.number="selected.ring_seconds" type="number" label="Ring secs" />
                    <TextField v-model.number="selected.max_wait_seconds" type="number" label="Max wait secs" />
                  </div>
                  <Button variant="secondary" size="sm" style="margin-bottom: 1rem" @click="saveSettings">
                    Save settings
                  </Button>

                  <div class="text-light" style="font-weight: 500; margin-bottom: 0.375rem">
                    Members (position orders sequential ring)
                  </div>
                  <div class="table" style="border: 1px solid var(--border); border-radius: var(--radius-large); overflow: hidden">
                    <table>
                      <tbody>
                        <tr v-for="m in selected.members" :key="m.agent_id">
                          <td class="text-lighter" style="width: 2.5rem">{{ m.position }}</td>
                          <td class="mono">{{ m.sip_username }}</td>
                          <td class="text-light">{{ m.display_name }}</td>
                          <td>
                            <div class="hstack row-actions">
                              <Button variant="danger" size="sm" @click="removeMember(m.agent_id)">Remove</Button>
                            </div>
                          </td>
                        </tr>
                        <tr v-if="!selected.members?.length">
                          <td colspan="4" class="align-center text-lighter">No members yet</td>
                        </tr>
                      </tbody>
                    </table>
                  </div>

                  <div class="flex gap-2" style="align-items: flex-end; margin-top: 0.75rem">
                    <div style="flex: 1 1 auto">
                      <Select
                        v-model="addAgentId"
                        label="Add agent"
                        placeholder="Select an agent…"
                        :options="addAgentOptions"
                      />
                    </div>
                    <div style="width: 6rem">
                      <TextField v-model.number="addPosition" type="number" label="Position" />
                    </div>
                    <Button :disabled="!addAgentId" @click="addMember">Add</Button>
                  </div>
                </td>
              </tr>
            </template>
            <tr v-if="!loading && teams.length === 0">
              <td colspan="5" class="align-center text-lighter">No teams yet</td>
            </tr>
            <tr v-if="loading">
              <td colspan="5" class="align-center text-lighter">Loading…</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Modal v-model:open="showForm" title="New team">
      <form id="team-form" class="vstack" @submit.prevent="create">
        <TextField v-model="form.name" label="Name" required placeholder="Sales" />
        <Select v-model="form.strategy" label="Strategy" :options="strategyOptions" />
        <div class="grid grid-2">
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

<style scoped>
.row-actions { justify-content: flex-end; flex-wrap: nowrap; }
</style>

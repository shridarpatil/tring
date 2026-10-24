<script setup lang="ts">
import { ref, onMounted } from "vue";
import { useRouter } from "vue-router";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import type { Team, TeamStrategy } from "../api/types";
import { Button, DataTable, Modal, Panel, Select, TextField, type Column } from "../components/ui";
import { useQueues } from "../composables/useQueues";
import { strategyLabel, strategyOptions } from "../lib/teams";

const router = useRouter();
const queues = useQueues();

const teams = ref<Team[]>([]);
const loading = ref(true);

const cols: Column[] = [
  { key: "name", label: "Name" },
  { key: "strategy", label: "Strategy" },
  { key: "ring_seconds", label: "Ring", align: "right" },
  { key: "max_wait_seconds", label: "Max wait", align: "right" },
  { key: "waiting", label: "Waiting", align: "right" },
];

const showForm = ref(false);
const form = ref<{ name: string; strategy: TeamStrategy; ring_seconds: number; max_wait_seconds: number }>({
  name: "",
  strategy: "ring_all",
  ring_seconds: 20,
  max_wait_seconds: 120,
});
const saving = ref(false);

async function load() {
  loading.value = true;
  try {
    teams.value = await api.get<Team[]>("/v1/teams");
  } catch (e) {
    toast.error(errMessage(e, "failed to load"));
  } finally {
    loading.value = false;
  }
}

async function create() {
  saving.value = true;
  try {
    const t = await api.post<Team>("/v1/teams", form.value);
    showForm.value = false;
    toast.success("Team created");
    openTeam(t.name);
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
  } finally {
    saving.value = false;
  }
}

function openTeam(name: string) {
  router.push({ name: "team", params: { name } });
}

function waiting(name: string) {
  return queues.value.queues.find((q) => q.team === name)?.waiting ?? 0;
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

    <Panel>
      <DataTable
        :columns="cols"
        :rows="teams"
        row-key="name"
        :loading="loading"
        empty-text="No teams yet — click New team to start."
      >
        <template #cell-name="{ value }">
          <a href="#" class="team-link" @click.prevent="openTeam(String(value))">{{ value }}</a>
        </template>
        <template #cell-strategy="{ value }">{{ strategyLabel(String(value)) }}</template>
        <template #cell-ring_seconds="{ value }"><span class="text-light">{{ value }}s</span></template>
        <template #cell-max_wait_seconds="{ value }"><span class="text-light">{{ value }}s</span></template>
        <template #cell-waiting="{ row }">
          <span :class="waiting(row.name) ? '' : 'text-lighter'">{{ waiting(row.name) }}</span>
        </template>
        <template #row-actions="{ row }">
          <div class="hstack row-actions">
            <Button variant="secondary" size="sm" @click="openTeam(row.name)">Manage</Button>
          </div>
        </template>
      </DataTable>
    </Panel>

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
.team-link { font-weight: 500; color: inherit; text-decoration: none; }
.team-link:hover { text-decoration: underline; }
</style>

<script setup lang="ts">
import { ref } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { usePoll } from "../composables/usePoll";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { agentSignalState } from "../lib/status";
import type { Agent, AgentToken } from "../api/types";
import {
  Button,
  Panel,
  DataTable,
  type Column,
  SignalDot,
  Badge,
  Modal,
  TextField,
  CopyButton,
} from "../components/ui";

const { items: agents, loading, load } = useList<Agent>("/v1/agents");
// Live status: silent re-poll every 10s so online/offline tracks registrations.
usePoll(() => load({ silent: true }), 10_000, { immediate: false });

const showForm = ref(false);
const form = ref({ sip_username: "", display_name: "" });
const saving = ref(false);

const tokenFor = ref<string>("");
const token = ref<AgentToken | null>(null);

const cols: Column[] = [
  { key: "status", label: "Status" },
  { key: "available", label: "Availability" },
  { key: "sip_username", label: "SIP username", mono: true },
  { key: "display_name", label: "Display name" },
  { key: "created_at", label: "Created", align: "right" },
];

async function create() {
  saving.value = true;
  try {
    await api.post<Agent>("/v1/agents", form.value);
    form.value = { sip_username: "", display_name: "" };
    showForm.value = false;
    await load();
    toast.success("Agent created");
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
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
    toast.error(errMessage(e, "token failed"));
  }
}

// Flip availability_for_calls (the Away/Break toggle). Same endpoint the
// integrator's backend uses; an operator can also set it here.
async function setAvailability(a: Agent, available: boolean) {
  try {
    const updated = await api.post<Agent>(`/v1/agents/${a.id}/availability`, { available });
    const i = agents.value.findIndex((x) => x.id === a.id);
    if (i >= 0) agents.value[i] = { ...agents.value[i], ...updated };
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
  }
}
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold tracking-tight">Agents</h1>
      <Button @click="showForm = true">New agent</Button>
    </div>

    <div v-if="token" class="rounded-xl border border-live/30 bg-live-soft p-4">
      <div class="flex items-center justify-between gap-3">
        <div class="text-sm">
          Token for <span class="font-mono">{{ tokenFor }}</span>
          <span class="text-ink-muted">
            · expires {{ new Date(token.expires_at).toLocaleTimeString() }}</span
          >
        </div>
        <CopyButton :value="token.token" label="Copy token" />
      </div>
      <code class="block mt-2 break-all text-xs font-mono text-ink-muted">{{ token.token }}</code>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="agents" :loading="loading" empty-text="No agents yet">
        <template #cell-status="{ row }">
          <SignalDot :state="agentSignalState(row.status)" />
        </template>
        <template #cell-available="{ row }">
          <button
            class="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent rounded-md"
            :title="row.available_for_calls ? 'Click to set Away' : 'Click to set Available'"
            @click="setAvailability(row, !row.available_for_calls)"
          >
            <Badge :tone="row.available_for_calls ? 'success' : 'warning'">
              {{ row.available_for_calls ? "Available" : "Away" }}
            </Badge>
          </button>
        </template>
        <template #cell-created_at="{ value }">
          <span class="text-ink-muted">{{ new Date(String(value)).toLocaleDateString() }}</span>
        </template>
        <template #row-actions="{ row }">
          <Button variant="ghost" size="sm" @click="mintToken(row)">Get token</Button>
        </template>
      </DataTable>
    </Panel>

    <Modal v-model:open="showForm" title="New agent">
      <form id="agent-form" class="space-y-4" @submit.prevent="create">
        <TextField v-model="form.sip_username" label="SIP username" mono required placeholder="alice" />
        <TextField v-model="form.display_name" label="Display name" placeholder="Alice" />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="agent-form" :loading="saving">Create</Button>
      </template>
    </Modal>
  </div>
</template>

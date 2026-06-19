<script setup lang="ts">
import { ref } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import type { Webhook } from "../api/types";
import {
  Button,
  Panel,
  DataTable,
  type Column,
  Badge,
  Modal,
  TextField,
  Toggle,
  CopyButton,
} from "../components/ui";

const { items: webhooks, loading, load } = useList<Webhook>("/v1/webhooks");

const showForm = ref(false);
const saving = ref(false);
const form = ref({ url: "", events: [] as string[] });
const newSecret = ref("");

const eventTypes = ["call.ringing", "call.answered", "call.ended"];

const cols: Column[] = [
  { key: "url", label: "URL", mono: true },
  { key: "events", label: "Events" },
  { key: "created_at", label: "Created", align: "right" },
];

function toggleEvent(ev: string, on: boolean) {
  if (on) {
    if (!form.value.events.includes(ev)) form.value.events.push(ev);
  } else {
    form.value.events = form.value.events.filter((e) => e !== ev);
  }
}

async function create() {
  saving.value = true;
  newSecret.value = "";
  try {
    const w = await api.post<Webhook>("/v1/webhooks", form.value);
    if (w.secret) newSecret.value = w.secret;
    form.value = { url: "", events: [] };
    showForm.value = false;
    await load();
    toast.success("Webhook created");
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
  } finally {
    saving.value = false;
  }
}

async function remove(w: Webhook) {
  if (!confirm(`Delete webhook ${w.url}?`)) return;
  try {
    await api.del(`/v1/webhooks/${w.id}`);
    await load();
    toast.success("Webhook deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold tracking-tight">Webhooks</h1>
      <Button @click="showForm = true">Add webhook</Button>
    </div>

    <div v-if="newSecret" class="rounded-xl border border-live/30 bg-live-soft p-4">
      <div class="flex items-center justify-between gap-3">
        <div class="text-sm">Signing secret — shown once, save it now</div>
        <CopyButton :value="newSecret" label="Copy secret" />
      </div>
      <code class="block mt-2 break-all text-xs font-mono text-ink-muted">{{ newSecret }}</code>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="webhooks" :loading="loading" empty-text="No webhooks">
        <template #cell-events="{ row }">
          <div v-if="row.events.length" class="flex flex-wrap gap-1.5">
            <Badge v-for="ev in row.events" :key="ev" tone="neutral">{{ ev }}</Badge>
          </div>
          <Badge v-else tone="accent">all</Badge>
        </template>
        <template #cell-created_at="{ value }">
          <span class="text-ink-muted">{{ new Date(String(value)).toLocaleDateString() }}</span>
        </template>
        <template #row-actions="{ row }">
          <Button variant="danger" size="sm" @click="remove(row)">Delete</Button>
        </template>
      </DataTable>
    </Panel>

    <Modal v-model:open="showForm" title="Add webhook">
      <form id="webhook-form" class="space-y-4" @submit.prevent="create">
        <TextField
          v-model="form.url"
          label="Delivery URL"
          type="url"
          required
          placeholder="https://your-crm.com/tring/webhook"
        />
        <div>
          <span class="text-sm font-medium text-ink">Events</span>
          <div class="mt-2 space-y-2">
            <Toggle
              v-for="ev in eventTypes"
              :key="ev"
              :model-value="form.events.includes(ev)"
              :label="ev"
              @update:model-value="(on: boolean) => toggleEvent(ev, on)"
            />
          </div>
          <p class="text-xs text-ink-faint mt-2">Leave all off to receive every event.</p>
        </div>
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="webhook-form" :loading="saving">Create</Button>
      </template>
    </Modal>
  </div>
</template>

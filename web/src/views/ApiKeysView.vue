<script setup lang="ts">
import { ref } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import type { ApiKey } from "../api/types";
import {
  Button,
  Panel,
  DataTable,
  type Column,
  Badge,
  Modal,
  TextField,
  CopyButton,
} from "../components/ui";

const { items: keys, loading, load } = useList<ApiKey>("/v1/api-keys");

const showForm = ref(false);
const creating = ref(false);
const newName = ref("");
const newKey = ref(""); // plaintext, shown once after create

const cols: Column[] = [
  { key: "name", label: "Name" },
  { key: "key_prefix", label: "Key", mono: true },
  { key: "created_at", label: "Created" },
  { key: "last_used_at", label: "Last used" },
  { key: "status", label: "Status" },
];

async function create() {
  creating.value = true;
  newKey.value = "";
  try {
    const k = await api.post<ApiKey>("/v1/api-keys", { name: newName.value });
    if (k.key) newKey.value = k.key;
    newName.value = "";
    showForm.value = false;
    await load();
    toast.success("API key generated");
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
  } finally {
    creating.value = false;
  }
}

async function revoke(k: ApiKey) {
  if (!confirm(`Revoke "${k.name}" (${k.key_prefix}…)? Anything using it will stop working.`)) return;
  try {
    await api.del(`/v1/api-keys/${k.id}`);
    await load();
    toast.success("API key revoked");
  } catch (e) {
    toast.error(errMessage(e, "revoke failed"));
  }
}

function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "—";
}
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold tracking-tight">API keys</h1>
      <Button @click="showForm = true">Generate key</Button>
    </div>

    <p class="text-sm text-ink-muted">
      Server-to-server credentials (<code class="font-mono text-ink">tk_live_…</code>) for your
      backend — e.g. minting calling credentials. Send as
      <code class="font-mono text-ink">Authorization: Bearer &lt;key&gt;</code>. Never expose a key
      in a browser.
    </p>

    <!-- Plaintext shown exactly once -->
    <div v-if="newKey" class="rounded-xl border border-live/30 bg-live-soft p-4">
      <div class="flex items-center justify-between gap-3">
        <div class="text-sm">New API key — copy it now, it won't be shown again</div>
        <CopyButton :value="newKey" label="Copy key" />
      </div>
      <code class="block mt-2 break-all text-xs font-mono text-ink-muted">{{ newKey }}</code>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="keys" :loading="loading" empty-text="No API keys yet">
        <template #cell-name="{ row }">
          <span :class="row.revoked ? 'text-ink-faint' : ''">{{ row.name }}</span>
        </template>
        <template #cell-key_prefix="{ value }">
          <span class="text-ink-muted">{{ value }}…</span>
        </template>
        <template #cell-created_at="{ value }">
          <span class="text-ink-muted">{{ fmt(value as string | undefined) }}</span>
        </template>
        <template #cell-last_used_at="{ value }">
          <span class="text-ink-muted">{{ fmt(value as string | undefined) }}</span>
        </template>
        <template #cell-status="{ row }">
          <Badge :tone="row.revoked ? 'neutral' : 'success'">
            {{ row.revoked ? "revoked" : "active" }}
          </Badge>
        </template>
        <template #row-actions="{ row }">
          <Button v-if="!row.revoked" variant="danger" size="sm" @click="revoke(row)">Revoke</Button>
        </template>
      </DataTable>
    </Panel>

    <Modal v-model:open="showForm" title="Generate API key">
      <form id="apikey-form" class="space-y-2" @submit.prevent="create">
        <TextField
          v-model="newName"
          label="Name"
          placeholder="e.g. support-portal backend"
          hint="A label to recognise where this key is used."
        />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="apikey-form" :loading="creating">Generate</Button>
      </template>
    </Modal>
  </div>
</template>

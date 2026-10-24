<script setup lang="ts">
import { computed, ref } from "vue";
import { Plus, X } from "lucide-vue-next";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import type { Webhook } from "../api/types";
import { Button, Panel, DataTable, type Column, Badge, Modal, TextField, Toggle, CopyButton } from "../components/ui";
import { confirmDialog } from "../lib/confirm";

const { items: webhooks, loading, load } = useList<Webhook>("/v1/webhooks");

// One modal for add and edit. Header values are write-only: editing shows the
// saved names, and only a "Replace headers" edit sends a new set.
const showForm = ref(false);
const editing = ref<Webhook | null>(null);
const saving = ref(false);
const form = ref({ name: "", url: "", active: true });
const headerRows = ref<{ key: string; value: string }[]>([]);
const replaceHeaders = ref(false);
const newSecret = ref("");

const cols: Column[] = [
  { key: "name", label: "Name" },
  { key: "url", label: "URL", mono: true },
  { key: "header_names", label: "Headers" },
  { key: "active", label: "Active" },
];

const title = computed(() => (editing.value ? "Edit webhook" : "Add webhook"));

function openAdd() {
  editing.value = null;
  form.value = { name: "", url: "", active: true };
  headerRows.value = [];
  replaceHeaders.value = true;
  showForm.value = true;
}

function openEdit(w: Webhook) {
  editing.value = w;
  form.value = { name: w.name, url: w.url, active: w.active };
  headerRows.value = [];
  replaceHeaders.value = false;
  showForm.value = true;
}

function headersObject(): Record<string, string> {
  const h: Record<string, string> = {};
  for (const r of headerRows.value) {
    if (r.key.trim()) h[r.key.trim()] = r.value;
  }
  return h;
}

async function save() {
  saving.value = true;
  newSecret.value = "";
  try {
    const body: Record<string, unknown> = { ...form.value };
    if (replaceHeaders.value) body.headers = headersObject();
    if (editing.value) {
      await api.patch<Webhook>(`/v1/webhooks/${editing.value.id}`, body);
      toast.success("Webhook saved");
    } else {
      const w = await api.post<Webhook>("/v1/webhooks", body);
      if (w.secret) newSecret.value = w.secret;
      toast.success("Webhook created");
    }
    showForm.value = false;
    await load();
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  } finally {
    saving.value = false;
  }
}

async function setActive(w: Webhook, active: boolean) {
  try {
    await api.patch<Webhook>(`/v1/webhooks/${w.id}`, { active });
    await load();
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
  }
}

async function remove(w: Webhook) {
  if (!(await confirmDialog(`Delete webhook ${w.name}? IVR nodes that use it stop sending.`))) return;
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
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">Webhooks</h1>
      <Button @click="openAdd">Add webhook</Button>
    </div>
    <p class="text-light">
      Register a URL here, then pick it in an IVR flow: a node's <strong>Notify webhook</strong>, or the flow's
      <strong>On call end</strong>. A disabled webhook is never sent.
    </p>

    <div v-if="newSecret" role="alert" data-variant="warning">
      <div class="hstack justify-between">
        <div>Signing secret — shown once, save it now</div>
        <CopyButton :value="newSecret" label="Copy secret" />
      </div>
      <code class="mono reveal-code">{{ newSecret }}</code>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="webhooks" :loading="loading" empty-text="No webhooks">
        <template #cell-header_names="{ row }">
          <div v-if="row.header_names.length" class="chip-wrap">
            <Badge v-for="h in row.header_names" :key="h" tone="neutral">{{ h }}</Badge>
          </div>
          <span v-else class="text-lighter">—</span>
        </template>
        <template #cell-active="{ row }">
          <Toggle :model-value="row.active" @update:model-value="(on: boolean) => setActive(row, on)" />
        </template>
        <template #row-actions="{ row }">
          <div class="hstack">
            <Button variant="secondary" size="sm" @click="openEdit(row)">Edit</Button>
            <Button variant="danger" size="sm" @click="remove(row)">Delete</Button>
          </div>
        </template>
      </DataTable>
    </Panel>

    <Modal v-model:open="showForm" :title="title">
      <form id="webhook-form" class="vstack" @submit.prevent="save">
        <TextField v-model="form.name" label="Name" required placeholder="CRM call events" />
        <TextField
          v-model="form.url"
          label="Delivery URL"
          type="url"
          required
          placeholder="https://your-crm.com/tring/webhook"
        />
        <Toggle v-model="form.active" label="Active" />

        <div>
          <span style="font-size: var(--text-7); font-weight: 500">Headers</span>
          <div v-if="editing && !replaceHeaders" class="hstack justify-between" style="margin-top: 0.5rem">
            <span class="text-light">
              {{ editing.header_names.length ? editing.header_names.join(", ") : "None" }}
            </span>
            <Button variant="ghost" size="sm" type="button" @click="replaceHeaders = true">Replace headers</Button>
          </div>
          <template v-else>
            <div v-for="(r, i) in headerRows" :key="i" class="hstack header-row">
              <input v-model="r.key" class="mono" placeholder="Authorization" />
              <input v-model="r.value" class="mono" placeholder="Bearer …" />
              <button type="button" class="outline small" title="Remove" @click="headerRows.splice(i, 1)">
                <X :size="14" />
              </button>
            </div>
            <Button variant="ghost" size="sm" type="button" @click="headerRows.push({ key: '', value: '' })">
              <Plus :size="14" /> Add header
            </Button>
            <p class="text-lighter" style="margin-top: 0.5rem">
              Sent with every request, for example an auth token. Values are stored encrypted and never shown
              again.<template v-if="editing"> Saving replaces all of this webhook's headers.</template>
            </p>
          </template>
        </div>
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="webhook-form" :loading="saving">{{ editing ? "Save" : "Create" }}</Button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.header-row {
  margin-top: 0.5rem;
  gap: 0.5rem;
}
.header-row input {
  flex: 1;
  min-width: 0;
}
</style>

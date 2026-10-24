<script setup lang="ts">
// NumbersView — register, edit and delete DIDs and bind them to a trunk.
// Outbound numbers MUST reference a trunk (where the call lands);
// inbound/masking numbers can stand alone.
import { ref, onMounted, computed } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { confirmDialog } from "../lib/confirm";
import type { PhoneNumber, Trunk, Flow, Paginated } from "../api/types";
import {
  Button,
  Panel,
  DataTable,
  type Column,
  Badge,
  Modal,
  TextField,
  Select,
} from "../components/ui";

const numbers = ref<PhoneNumber[]>([]);
const trunks = ref<Trunk[]>([]);
const flows = ref<Flow[]>([]);
const loading = ref(true);

const showForm = ref(false);
const saving = ref(false);
// The number being edited; null = the modal registers a new one.
const editing = ref<PhoneNumber | null>(null);
const form = ref<{
  e164: string;
  type: "inbound" | "outbound" | "masking";
  trunk_id: string;
  // Flow assigned to inbound calls on this DID. Empty string = no
  // flow; the create endpoint accepts a missing field as "no flow",
  // so we just drop it from the body when empty.
  flow_id: string;
  priority: number;
}>({ e164: "", type: "outbound", trunk_id: "", flow_id: "", priority: 100 });

const types = ["outbound", "inbound", "masking"];

const cols: Column[] = [
  { key: "e164", label: "Number", mono: true },
  { key: "type", label: "Type" },
  { key: "trunk", label: "Trunk" },
  { key: "flow", label: "Flow" },
  { key: "priority", label: "Priority" },
  { key: "active", label: "Active" },
];

const typeOptions = computed(() => types.map((t) => ({ value: t, label: t })));

const trunkOptions = computed(() => [
  { value: "", label: "— none —" },
  ...trunks.value
    .filter((x) => x.active)
    .map((t) => ({ value: t.id, label: `${t.name}${t.status ? ` (${t.status})` : ""}` })),
]);

const flowOptions = computed(() => [
  { value: "", label: "— none —" },
  ...flows.value.filter((x) => x.is_active).map((f) => ({ value: f.id, label: f.name })),
]);

// Per-row flow assignment options (lists all flows, not just active ones).
const rowFlowOptions = computed(() => [
  { value: "", label: "— none —" },
  ...flows.value.map((f) => ({ value: f.id, label: f.name })),
]);

async function load() {
  loading.value = true;
  try {
    const [n, t, f] = await Promise.all([
      api.get<PhoneNumber[]>("/v1/numbers"),
      api.get<Trunk[]>("/v1/trunks"),
      api.get<Paginated<Flow>>("/v1/flows"),
    ]);
    numbers.value = n;
    trunks.value = t;
    flows.value = f.items;
  } catch (e) {
    toast.error(errMessage(e, "failed to load"));
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editing.value = null;
  form.value = { e164: "", type: "outbound", trunk_id: "", flow_id: "", priority: 100 };
  showForm.value = true;
}

function openEdit(n: PhoneNumber) {
  editing.value = n;
  form.value = {
    e164: n.e164,
    type: n.type as "inbound" | "outbound" | "masking",
    trunk_id: n.trunk_id ?? "",
    flow_id: n.flow_id ?? "",
    priority: n.priority,
  };
  showForm.value = true;
}

async function submit() {
  if (editing.value) await update(editing.value);
  else await create();
}

// update sends only the fields that changed, so an untouched field is never
// re-validated. Flow is assigned from the row, not here.
async function update(n: PhoneNumber) {
  const body: Record<string, unknown> = {};
  if (form.value.e164 !== n.e164) body.e164 = form.value.e164;
  if (form.value.type !== n.type) body.type = form.value.type;
  if (form.value.trunk_id && form.value.trunk_id !== n.trunk_id) body.trunk_id = form.value.trunk_id;
  if (form.value.priority !== n.priority) body.priority = form.value.priority;
  if (Object.keys(body).length === 0) {
    showForm.value = false;
    return;
  }
  saving.value = true;
  try {
    await api.patch(`/v1/numbers/${n.id}`, body);
    showForm.value = false;
    await load();
    toast.success("Number updated");
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
  } finally {
    saving.value = false;
  }
}

async function remove(n: PhoneNumber) {
  if (!(await confirmDialog(`Delete number ${n.e164}?`))) return;
  try {
    await api.del(`/v1/numbers/${n.id}`);
    await load();
    toast.success("Number deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}

async function create() {
  saving.value = true;
  try {
    const body: Record<string, unknown> = {
      e164: form.value.e164,
      type: form.value.type,
      priority: form.value.priority,
    };
    if (form.value.trunk_id) body.trunk_id = form.value.trunk_id;
    if (form.value.flow_id) body.flow_id = form.value.flow_id;
    await api.post("/v1/numbers", body);
    form.value = { e164: "", type: "outbound", trunk_id: "", flow_id: "", priority: 100 };
    showForm.value = false;
    await load();
    toast.success("Number registered");
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
  } finally {
    saving.value = false;
  }
}

async function toggleActive(n: PhoneNumber) {
  try {
    await api.patch(`/v1/numbers/${n.id}`, { active: !n.active });
    await load();
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
  }
}

// assignFlow drives the per-row Flow select: empty value means
// explicit unassign (clear_flow=true), otherwise set the chosen flow.
async function assignFlow(n: PhoneNumber, newId: string) {
  try {
    if (newId === "") {
      await api.patch(`/v1/numbers/${n.id}`, { clear_flow: true });
    } else {
      await api.patch(`/v1/numbers/${n.id}`, { flow_id: newId });
    }
    await load();
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
  }
}

function trunkName(id: string | null): string {
  if (!id) return "—";
  const t = trunks.value.find((x) => x.id === id);
  return t ? t.name : id.slice(0, 8);
}

onMounted(load);
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">Numbers</h1>
      <Button @click="openCreate">Register number</Button>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="numbers" :loading="loading" empty-text="No numbers registered">
        <template #cell-trunk="{ row }">
          <span class="text-light">{{ trunkName(row.trunk_id) }}</span>
        </template>
        <template #cell-flow="{ row }">
          <Select
            :model-value="row.flow_id || ''"
            :options="rowFlowOptions"
            @update:model-value="assignFlow(row, $event)"
          />
        </template>
        <template #cell-active="{ row }">
          <Badge :tone="row.active ? 'success' : 'neutral'">{{ row.active ? "yes" : "no" }}</Badge>
        </template>
        <template #row-actions="{ row }">
          <div class="hstack">
            <Button variant="secondary" size="sm" @click="openEdit(row)">Edit</Button>
            <Button variant="secondary" size="sm" @click="toggleActive(row)">
              {{ row.active ? "Deactivate" : "Activate" }}
            </Button>
            <Button variant="danger" size="sm" @click="remove(row)">Delete</Button>
          </div>
        </template>
      </DataTable>
    </Panel>

    <Modal v-model:open="showForm" :title="editing ? 'Edit number' : 'Register number'">
      <form id="number-form" class="vstack" @submit.prevent="submit">
        <TextField v-model="form.e164" label="E.164" mono required placeholder="+918001234567" />
        <Select v-model="form.type" label="Type" :options="typeOptions" />
        <Select
          v-model="form.trunk_id"
          label="Trunk"
          :options="trunkOptions"
          :required="form.type === 'outbound'"
        />
        <Select v-if="!editing" v-model="form.flow_id" label="Flow" :options="flowOptions" />
        <TextField v-model.number="form.priority" label="Priority" type="number" />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="number-form" :loading="saving">
          {{ editing ? "Save" : "Register" }}
        </Button>
      </template>
    </Modal>
  </div>
</template>

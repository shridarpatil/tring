<script setup lang="ts">
// FlowsListView — list/create/duplicate/delete IVR flows via the Tring API.
import { ref } from "vue";
import { errMessage } from "../../lib/errors";
import { usePagedList } from "../../composables/usePagedList";
import { useRouter } from "vue-router";
import { Trash2, ArrowRight, Copy } from "lucide-vue-next";
import { api } from "../../api/client";
import { toast } from "../../lib/toast";
import type { Flow } from "../../api/types";
import {
  Button,
  Badge,
  IconButton,
  Modal,
  Panel,
  Pagination,
  DataTable,
  type Column,
  TextField,
} from "../../components/ui";
import { confirmDialog } from "../../lib/confirm";

const router = useRouter();

const {
  items: flows,
  page,
  pageSize,
  total,
  loading,
  load: refresh,
  setPage,
} = usePagedList<Flow>("/v1/flows");
const showCreate = ref(false);
const newName = ref("");
const creating = ref(false);
const dupSource = ref<Flow | null>(null);
const dupName = ref("");
const duplicating = ref(false);

const cols: Column[] = [
  { key: "name", label: "Name" },
  { key: "is_active", label: "Active" },
  { key: "nodes", label: "Nodes", align: "right" },
  { key: "updated_at", label: "Updated", align: "right" },
];

async function createFlow() {
  if (!newName.value.trim()) return;
  creating.value = true;
  try {
    const f = await api.post<Flow>("/v1/flows", { name: newName.value.trim() });
    router.push({ name: "flow-editor", params: { id: f.id } });
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
  } finally {
    creating.value = false;
  }
}

function openDuplicate(f: Flow) {
  dupSource.value = f;
  dupName.value = `${f.name} (copy)`;
}

// duplicateFlow copies a flow's graph into a new flow (e.g. to translate an
// IVR into another language). The copy starts as a draft so it can't take
// calls until it's been edited and activated.
async function duplicateFlow() {
  const src = dupSource.value;
  if (!src || !dupName.value.trim()) return;
  duplicating.value = true;
  try {
    const f = await api.post<Flow>("/v1/flows", {
      name: dupName.value.trim(),
      description: src.description,
      is_active: false,
      is_outgoing_end: src.is_outgoing_end,
      definition: src.definition,
      welcome_audio_url: src.welcome_audio_url,
    });
    dupSource.value = null;
    router.push({ name: "flow-editor", params: { id: f.id } });
  } catch (e) {
    toast.error(errMessage(e, "duplicate failed"));
  } finally {
    duplicating.value = false;
  }
}

async function deleteFlow(id: string) {
  if (!(await confirmDialog("Delete this flow?"))) return;
  try {
    await api.del(`/v1/flows/${id}`);
    await refresh();
    toast.success("Flow deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">IVR Flows</h1>
      <Button @click="showCreate = true">New flow</Button>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="flows" :loading="loading" empty-text="No flows yet — click New flow to start.">
        <template #cell-name="{ value }">
          <span style="font-weight: 500">{{ value }}</span>
        </template>
        <template #cell-is_active="{ value }">
          <Badge v-if="value" tone="success">Active</Badge>
          <Badge v-else>Draft</Badge>
        </template>
        <template #cell-nodes="{ row }">
          <span class="text-light">{{ row.definition?.nodes?.length || 0 }}</span>
        </template>
        <template #cell-updated_at="{ value }">
          <span class="text-light">{{ new Date(String(value)).toLocaleString() }}</span>
        </template>
        <template #row-actions="{ row }">
          <Button
            variant="ghost"
            size="sm"
            @click="router.push({ name: 'flow-editor', params: { id: row.id } })"
          >
            Edit <ArrowRight :size="12" />
          </Button>
          <IconButton title="Duplicate flow" size="sm" @click="openDuplicate(row as Flow)">
            <Copy :size="16" />
          </IconButton>
          <IconButton title="Delete flow" size="sm" variant="danger" @click="deleteFlow(row.id)">
            <Trash2 :size="16" />
          </IconButton>
        </template>
      </DataTable>
      <Pagination
        :page="page"
        :page-size="pageSize"
        :total="total"
        label="flows"
        @update:page="setPage"
      />
    </Panel>

    <p class="text-lighter">
      Flows persist via the API. The FreeSWITCH executor (Phase 3) is
      not wired yet — saving doesn't yet make inbound calls run the
      flow; the editor is fully functional for design.
    </p>

    <Modal v-model:open="showCreate" title="New flow">
      <form id="flow-form" class="vstack" @submit.prevent="createFlow">
        <TextField v-model="newName" label="Flow name" required placeholder="Main IVR" />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showCreate = false">Cancel</Button>
        <Button type="submit" form="flow-form" :loading="creating">Create &amp; edit</Button>
      </template>
    </Modal>

    <Modal
      :open="dupSource !== null"
      title="Duplicate flow"
      @update:open="(v: boolean) => { if (!v) dupSource = null; }"
    >
      <form id="dup-flow-form" class="vstack" @submit.prevent="duplicateFlow">
        <TextField v-model="dupName" label="New flow name" required />
      </form>
      <template #footer>
        <Button variant="ghost" @click="dupSource = null">Cancel</Button>
        <Button type="submit" form="dup-flow-form" :loading="duplicating">Duplicate &amp; edit</Button>
      </template>
    </Modal>
  </div>
</template>

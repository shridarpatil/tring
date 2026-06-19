<script setup lang="ts">
// FlowsListView — list/create/delete IVR flows via the Tring API.
import { ref } from "vue";
import { errMessage } from "../../lib/errors";
import { useList } from "../../composables/useList";
import { useRouter } from "vue-router";
import { Trash2, ArrowRight } from "lucide-vue-next";
import { api } from "../../api/client";
import { toast } from "../../lib/toast";
import type { Flow } from "../../api/types";
import {
  Button,
  Badge,
  IconButton,
  Modal,
  Panel,
  DataTable,
  type Column,
  TextField,
} from "../../components/ui";

const router = useRouter();

const { items: flows, loading, load: refresh } = useList<Flow>("/v1/flows");
const showCreate = ref(false);
const newName = ref("");
const creating = ref(false);

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

async function deleteFlow(id: string) {
  if (!confirm("Delete this flow?")) return;
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
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold tracking-tight">IVR Flows</h1>
      <Button @click="showCreate = true">New flow</Button>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="flows" :loading="loading" empty-text="No flows yet — click New flow to start.">
        <template #cell-name="{ value }">
          <span class="font-medium text-ink">{{ value }}</span>
        </template>
        <template #cell-is_active="{ value }">
          <Badge v-if="value" tone="success">Active</Badge>
          <Badge v-else>Draft</Badge>
        </template>
        <template #cell-nodes="{ row }">
          <span class="text-ink-muted">{{ row.definition?.nodes?.length || 0 }}</span>
        </template>
        <template #cell-updated_at="{ value }">
          <span class="text-ink-muted">{{ new Date(String(value)).toLocaleString() }}</span>
        </template>
        <template #row-actions="{ row }">
          <Button
            variant="ghost"
            size="sm"
            @click="router.push({ name: 'flow-editor', params: { id: row.id } })"
          >
            Edit <ArrowRight class="h-3 w-3" />
          </Button>
          <IconButton title="Delete flow" size="sm" variant="danger" @click="deleteFlow(row.id)">
            <Trash2 class="h-4 w-4" />
          </IconButton>
        </template>
      </DataTable>
    </Panel>

    <p class="text-xs text-ink-faint">
      Flows persist via the API. The FreeSWITCH executor (Phase 3) is
      not wired yet — saving doesn't yet make inbound calls run the
      flow; the editor is fully functional for design.
    </p>

    <Modal v-model:open="showCreate" title="New flow">
      <form id="flow-form" class="space-y-4" @submit.prevent="createFlow">
        <TextField v-model="newName" label="Flow name" required placeholder="Main IVR" />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showCreate = false">Cancel</Button>
        <Button type="submit" form="flow-form" :loading="creating">Create &amp; edit</Button>
      </template>
    </Modal>
  </div>
</template>

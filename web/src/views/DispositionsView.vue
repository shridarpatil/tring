<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import type { WrapUpLabel } from "../api/types";
import { Button, Panel, TextField, Toggle, Badge, DataTable } from "../components/ui";

interface LabelList {
  title: string;
  hint: string;
  path: string;
  items: WrapUpLabel[];
  showArchived: boolean;
  newName: string;
  editingId: string;
  editName: string;
}

const cols = [{ key: "name", label: "Name" }];

const lists = ref<LabelList[]>([
  { title: "Dispositions", hint: "The outcome an agent picks after a call. Pick exactly one.", path: "/v1/dispositions" },
  { title: "Tags", hint: "Free-form labels an agent can add to a call. Pick any number.", path: "/v1/call-tags" },
].map((l) => ({ ...l, items: [], showArchived: false, newName: "", editingId: "", editName: "" })));

async function load(l: LabelList) {
  try {
    l.items = await api.get<WrapUpLabel[]>(`${l.path}?include_archived=1`);
  } catch (e) {
    toast.error(errMessage(e, "failed to load"));
  }
}

async function add(l: LabelList) {
  const name = l.newName.trim();
  if (!name) return;
  try {
    await api.post(l.path, { name });
    l.newName = "";
    await load(l);
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
  }
}

function startEdit(l: LabelList, it: WrapUpLabel) {
  l.editingId = it.id;
  l.editName = it.name;
}

async function patch(l: LabelList, it: WrapUpLabel, body: { name?: string; archived?: boolean }) {
  try {
    await api.patch(`${l.path}/${it.id}`, body);
    l.editingId = "";
    await load(l);
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
  }
}

function rename(l: LabelList, it: WrapUpLabel) {
  const name = l.editName.trim();
  if (!name || name === it.name) {
    l.editingId = "";
    return;
  }
  return patch(l, it, { name });
}

function visible(l: LabelList) {
  return l.showArchived ? l.items : l.items.filter((i) => !i.archived);
}

onMounted(() => lists.value.forEach(load));
</script>

<template>
  <div class="view">
    <h1 class="page-title">Dispositions &amp; tags</h1>
    <p class="text-light">
      What agents record after a call. Archiving hides a label from agents but keeps it on past calls.
    </p>

    <Panel v-for="l in lists" :key="l.path" pad>
      <template #title>{{ l.title }}</template>
      <div class="vstack">
        <p class="text-light">{{ l.hint }}</p>
        <form class="hstack" @submit.prevent="add(l)">
          <TextField v-model="l.newName" placeholder="New name (max 64 characters)" />
          <Button type="submit" :disabled="!l.newName.trim()">Add</Button>
        </form>
        <Toggle v-model="l.showArchived" label="Show archived" />
        <DataTable :columns="cols" :rows="visible(l)" empty-text="Nothing here yet.">
          <template #cell-name="{ row }">
            <form v-if="l.editingId === row.id" class="hstack" @submit.prevent="rename(l, row)">
              <TextField v-model="l.editName" />
              <Button type="submit" size="sm">Save</Button>
              <Button variant="ghost" size="sm" @click="l.editingId = ''">Cancel</Button>
            </form>
            <template v-else>
              <span :class="row.archived ? 'text-lighter' : ''">{{ row.name }}</span>
              <Badge v-if="row.archived" tone="neutral">archived</Badge>
            </template>
          </template>
          <template #row-actions="{ row }">
            <div class="hstack">
              <Button variant="secondary" size="sm" @click="startEdit(l, row)">Rename</Button>
              <Button variant="secondary" size="sm" @click="patch(l, row, { archived: !row.archived })">
                {{ row.archived ? "Restore" : "Archive" }}
              </Button>
            </div>
          </template>
        </DataTable>
      </div>
    </Panel>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from "vue";
import { api } from "../api/client";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import type { Team, WrapUpLabel } from "../api/types";
import { Button, Panel, TextField, Toggle, Badge, Modal } from "../components/ui";

interface LabelList {
  title: string;
  hint: string;
  placeholder: string;
  path: string;
  items: WrapUpLabel[];
  showArchived: boolean;
  newName: string;
  editingId: string;
  editName: string;
}

const lists = ref<LabelList[]>([
  { title: "Dispositions", hint: "The outcome an agent picks. Exactly one.", placeholder: "New disposition…", path: "/v1/dispositions" },
  { title: "Tags", hint: "Labels an agent adds. Any number.", placeholder: "New tag…", path: "/v1/call-tags" },
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

async function patch(l: LabelList, it: WrapUpLabel, body: { name?: string; archived?: boolean; teams?: string[] }) {
  try {
    await api.patch(`${l.path}/${it.id}`, body);
    l.editingId = "";
    await load(l);
    return true;
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
    return false;
  }
}

// Teams editor: an entry is shared (no teams) or limited to the checked ones.
const teamNames = ref<string[]>([]);
const teamsFor = ref<{ list: LabelList; item: WrapUpLabel } | null>(null);
const teamsOpen = ref(false);
const pickedTeams = ref<string[]>([]);
const savingTeams = ref(false);

async function loadTeams() {
  try {
    teamNames.value = (await api.get<Team[]>("/v1/teams")).map((t) => t.name).sort((a, b) => a.localeCompare(b));
  } catch (e) {
    toast.error(errMessage(e, "failed to load teams"));
  }
}

function editTeams(list: LabelList, item: WrapUpLabel) {
  teamsFor.value = { list, item };
  pickedTeams.value = [...item.teams];
  teamsOpen.value = true;
}

async function saveTeams() {
  if (!teamsFor.value) return;
  savingTeams.value = true;
  const ok = await patch(teamsFor.value.list, teamsFor.value.item, { teams: pickedTeams.value });
  savingTeams.value = false;
  if (ok) teamsOpen.value = false;
}

function teamsLine(it: WrapUpLabel) {
  return it.teams.length ? it.teams.join(", ") : "All teams";
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

onMounted(() => {
  lists.value.forEach(load);
  loadTeams();
});
</script>

<template>
  <div class="view">
    <h1 class="page-title">Dispositions &amp; tags</h1>
    <p class="text-light">
      What agents record after a call. Archiving hides a label from agents but keeps it on past calls.
    </p>

    <div class="grid grid-2">
      <Panel v-for="l in lists" :key="l.path" pad>
        <template #title>{{ l.title }}</template>
        <template #actions>
          <Toggle v-model="l.showArchived" label="Show archived" />
        </template>
        <div class="vstack">
          <p class="text-light">{{ l.hint }}</p>
          <form class="hstack" @submit.prevent="add(l)">
            <TextField v-model="l.newName" :placeholder="l.placeholder" />
            <Button type="submit" :disabled="!l.newName.trim()">Add</Button>
          </form>
          <p v-if="!visible(l).length" class="text-lighter">Nothing here yet.</p>
          <div v-for="it in visible(l)" :key="it.id" class="hstack justify-between">
            <form v-if="l.editingId === it.id" class="hstack" @submit.prevent="rename(l, it)">
              <TextField v-model="l.editName" />
              <Button type="submit" size="sm">Save</Button>
              <Button variant="ghost" size="sm" @click="l.editingId = ''">Cancel</Button>
            </form>
            <template v-else>
              <div>
                <span :class="it.archived ? 'text-lighter' : ''">{{ it.name }}</span>
                <Badge v-if="it.archived" tone="neutral">archived</Badge>
                <div class="text-light">{{ teamsLine(it) }}</div>
              </div>
              <div class="hstack">
                <Button variant="secondary" size="sm" @click="editTeams(l, it)">Teams</Button>
                <Button variant="secondary" size="sm" @click="startEdit(l, it)">Rename</Button>
                <Button variant="secondary" size="sm" @click="patch(l, it, { archived: !it.archived })">
                  {{ it.archived ? "Restore" : "Archive" }}
                </Button>
              </div>
            </template>
          </div>
        </div>
      </Panel>
    </div>

    <Modal v-model:open="teamsOpen" size="sm" :title="teamsFor ? `Teams for ${teamsFor.item.name}` : 'Teams'">
      <form id="label-teams-form" class="vstack" @submit.prevent="saveTeams">
        <label class="hstack gap-2">
          <input type="checkbox" :checked="!pickedTeams.length" :disabled="!pickedTeams.length" @change="pickedTeams = []" />
          All teams (shared)
        </label>
        <label v-for="name in teamNames" :key="name" class="hstack gap-2">
          <input v-model="pickedTeams" type="checkbox" :value="name" />
          {{ name }}
        </label>
        <p v-if="!teamNames.length" class="text-lighter">No teams yet.</p>
      </form>
      <template #footer>
        <Button variant="ghost" @click="teamsOpen = false">Cancel</Button>
        <Button type="submit" form="label-teams-form" :loading="savingTeams">Save</Button>
      </template>
    </Modal>
  </div>
</template>

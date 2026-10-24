<script setup lang="ts">
// TeamDetailView — one team's settings and ordered member list (/teams/:name).
import { ref, computed, watch } from "vue";
import { useRoute, useRouter, RouterLink } from "vue-router";
import { ArrowUp, ArrowDown } from "lucide-vue-next";
import { errMessage } from "../lib/errors";
import { api, ApiError } from "../api/client";
import { toast } from "../lib/toast";
import type { Agent, Team, TeamMember } from "../api/types";
import { Button, Badge, IconButton, Panel, Select, TextField } from "../components/ui";
import AudioPicker from "../components/AudioPicker.vue";
import { confirmDialog } from "../lib/confirm";
import { strategies, strategyOptions } from "../lib/teams";

const route = useRoute();
const router = useRouter();

const team = ref<Team | null>(null);
const members = ref<TeamMember[]>([]);
const agents = ref<Agent[]>([]);
const loading = ref(true);
const notFound = ref(false);

// Editable copy of the settings; "dirty" compares it against the saved team.
// The built-in stream sentinel and "" both mean "use the tenant hold music".
const BUILTIN_MOH = "local_stream://moh";
const form = ref({ strategy: "ring_all", ring_seconds: 20, max_wait_seconds: 120, moh_sound: "" });
const saving = ref(false);

function formFrom(t: Team) {
  return {
    strategy: t.strategy,
    ring_seconds: t.ring_seconds,
    max_wait_seconds: t.max_wait_seconds,
    moh_sound: t.moh_sound === BUILTIN_MOH ? "" : t.moh_sound,
  };
}

const dirty = computed(
  () => !!team.value && JSON.stringify(form.value) !== JSON.stringify(formFrom(team.value)),
);
const strategyHint = computed(() => strategies.find((s) => s.value === form.value.strategy)?.hint ?? "");
// Order only matters to sequential ring, so reorder controls show only then.
const ordered = computed(() => team.value?.strategy === "sequential");

const addAgentId = ref("");
const addAgentOptions = computed(() => {
  const inTeam = new Set(members.value.map((m) => m.agent_id));
  return agents.value
    .filter((a) => !inTeam.has(a.id))
    .map((a) => ({ value: a.id, label: a.display_name || a.sip_username }));
});

const teamPath = computed(() => `/v1/teams/${encodeURIComponent(String(route.params.name))}`);

async function load() {
  loading.value = true;
  notFound.value = false;
  try {
    const [t, a] = await Promise.all([api.get<Team>(teamPath.value), api.get<Agent[]>("/v1/agents")]);
    team.value = t;
    members.value = t.members ?? [];
    agents.value = a;
    form.value = formFrom(t);
  } catch (e) {
    team.value = null;
    if (e instanceof ApiError && e.status === 404) notFound.value = true;
    else toast.error(errMessage(e, "load team failed"));
  } finally {
    loading.value = false;
  }
}

async function saveSettings() {
  if (!team.value) return;
  saving.value = true;
  try {
    const updated = await api.patch<Team>(teamPath.value, form.value);
    team.value = updated;
    form.value = formFrom(updated);
    toast.success("Settings saved");
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  } finally {
    saving.value = false;
  }
}

async function addMember() {
  if (!addAgentId.value) return;
  try {
    const position = (members.value.at(-1)?.position ?? 0) + 1;
    members.value =
      (await api.post<TeamMember[]>(`${teamPath.value}/agents`, { agent_id: addAgentId.value, position })) ?? [];
    addAgentId.value = "";
  } catch (e) {
    toast.error(errMessage(e, "add failed"));
  }
}

async function removeMember(m: TeamMember) {
  try {
    await api.del<void>(`${teamPath.value}/agents/${m.agent_id}`);
    members.value = members.value.filter((x) => x.agent_id !== m.agent_id);
  } catch (e) {
    toast.error(errMessage(e, "remove failed"));
  }
}

// move swaps a member with its neighbour, then renumbers the list 1..n and
// upserts only the members whose position changed (positions may have gaps or
// ties from earlier edits, so a plain swap of two numbers isn't enough).
const moving = ref(false);
async function move(i: number, delta: -1 | 1) {
  const j = i + delta;
  if (j < 0 || j >= members.value.length) return;
  const next = [...members.value];
  [next[i], next[j]] = [next[j], next[i]];
  const changed = next
    .map((m, k) => ({ ...m, position: k + 1 }))
    .filter((m, k) => m.position !== next[k].position);
  moving.value = true;
  try {
    let latest: TeamMember[] | null = null;
    for (const m of changed) {
      latest = await api.post<TeamMember[]>(`${teamPath.value}/agents`, {
        agent_id: m.agent_id,
        position: m.position,
      });
    }
    if (latest) members.value = latest;
  } catch (e) {
    toast.error(errMessage(e, "reorder failed"));
    await load();
  } finally {
    moving.value = false;
  }
}

async function destroy() {
  if (!team.value) return;
  if (!(await confirmDialog(`Delete team "${team.value.name}"? Flows referencing it will fall back to no-answer.`)))
    return;
  try {
    await api.del<void>(teamPath.value);
    toast.success("Team deleted");
    router.push({ name: "teams" });
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}

watch(() => route.params.name, load, { immediate: true });
</script>

<template>
  <div class="view team-detail">
    <div class="row-between">
      <h1 class="page-title">
        <RouterLink :to="{ name: 'teams' }" class="crumb">Teams</RouterLink>
        <span class="crumb-sep">›</span>
        {{ route.params.name }}
      </h1>
      <Button v-if="team" variant="danger" @click="destroy">Delete team</Button>
    </div>

    <p v-if="loading" class="text-lighter">Loading…</p>

    <div v-else-if="notFound" class="card align-center">
      <p class="text-light">Team "{{ route.params.name }}" not found.</p>
      <RouterLink :to="{ name: 'teams' }">Back to teams</RouterLink>
    </div>

    <template v-else-if="team">
      <Panel pad>
        <template #title>Settings</template>
        <form class="vstack" @submit.prevent="saveSettings">
          <div class="grid grid-3">
            <Select v-model="form.strategy" label="Strategy" :options="strategyOptions" />
            <TextField v-model.number="form.ring_seconds" type="number" label="Ring secs" />
            <TextField v-model.number="form.max_wait_seconds" type="number" label="Max wait secs" />
          </div>
          <p class="text-lighter hint">{{ strategyHint }}</p>
          <div>
            <div class="field-label">Hold music</div>
            <AudioPicker v-model="form.moh_sound" default-label="Default (tenant hold music)" />
          </div>
          <div class="hstack" style="justify-content: flex-end">
            <Button type="submit" :disabled="!dirty" :loading="saving">Save settings</Button>
          </div>
        </form>
      </Panel>

      <Panel>
        <template #title>
          Members ({{ members.length }})
          <span v-if="ordered" class="text-lighter" style="font-weight: 400"> · rings top to bottom</span>
        </template>
        <div class="table">
          <table>
            <tbody>
              <tr v-for="(m, i) in members" :key="m.agent_id">
                <td class="text-lighter" style="width: 2.5rem">{{ i + 1 }}</td>
                <td class="mono">{{ m.sip_username }}</td>
                <td class="text-light">{{ m.display_name }}</td>
                <td>
                  <Badge :tone="m.available_for_calls ? 'success' : 'warning'">
                    {{ m.available_for_calls ? "Available" : "Away" }}
                  </Badge>
                </td>
                <td>
                  <div class="hstack row-actions">
                    <template v-if="ordered">
                      <IconButton title="Move up" size="sm" :disabled="i === 0 || moving" @click="move(i, -1)">
                        <ArrowUp :size="14" />
                      </IconButton>
                      <IconButton
                        title="Move down"
                        size="sm"
                        :disabled="i === members.length - 1 || moving"
                        @click="move(i, 1)"
                      >
                        <ArrowDown :size="14" />
                      </IconButton>
                    </template>
                    <Button variant="danger" size="sm" @click="removeMember(m)">Remove</Button>
                  </div>
                </td>
              </tr>
              <tr v-if="!members.length">
                <td colspan="5" class="align-center text-lighter">No members yet. Add an agent below.</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="flex gap-2 add-row">
          <div style="flex: 1 1 auto">
            <Select v-model="addAgentId" label="Add agent" placeholder="Select an agent…" :options="addAgentOptions" />
          </div>
          <Button :disabled="!addAgentId" @click="addMember">Add</Button>
        </div>
      </Panel>
    </template>
  </div>
</template>

<style scoped>
.team-detail { width: 100%; max-width: 56rem; margin-inline: auto; }
.crumb { color: var(--muted-foreground); text-decoration: none; font-weight: 400; }
.crumb:hover { color: var(--foreground); }
.crumb-sep { color: var(--muted-foreground); margin: 0 0.375rem; font-weight: 400; }
.hint { margin: 0; font-size: var(--text-8); }
.field-label { font-size: var(--text-7); font-weight: 500; margin-bottom: 0.375rem; }
.row-actions { justify-content: flex-end; flex-wrap: nowrap; }
.add-row { align-items: flex-end; padding: 1rem 1.25rem; border-top: 1px solid var(--border); }
.add-row :deep([data-field]) { margin-bottom: 0; }
</style>

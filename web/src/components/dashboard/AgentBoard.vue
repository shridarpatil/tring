<script setup lang="ts">
// AgentBoard — agents grouped by live state, with a name search and a team
// filter. Each group shows its first 8 until "Show all N" is pressed.
import { computed, reactive, ref, watch } from "vue";
import type { Agent, Team } from "../../api/types";
import { api } from "../../api/client";
import { errMessage } from "../../lib/errors";
import { toast } from "../../lib/toast";
import { Panel, SignalDot } from "../ui";

const props = defineProps<{ agents: Agent[]; teams: string[] }>();

const PER_GROUP = 8;
const GROUPS = [
  { key: "on_call", label: "On call" },
  { key: "online", label: "Available" },
  { key: "away", label: "Away" },
  { key: "offline", label: "Offline" },
] as const;
type GroupKey = (typeof GROUPS)[number]["key"];

const search = ref("");
const team = ref("");
const expanded = reactive<Record<string, boolean>>({});

// Team membership isn't on the stream; fetch a team's members once, when it is
// first picked in the filter, and keep them for this page's lifetime.
const members = reactive<Record<string, Set<string>>>({});
watch(team, async (name) => {
  if (!name || members[name]) return;
  try {
    const t = await api.get<Team>(`/v1/teams/${encodeURIComponent(name)}`);
    members[name] = new Set((t.members ?? []).map((m) => m.agent_id));
  } catch (e) {
    toast.error(errMessage(e, "Could not load team members"));
  }
});

const groupOf = (a: Agent): GroupKey =>
  a.status === "on_call" || a.status === "online" || a.status === "away" ? a.status : "offline";

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase();
  const inTeam = team.value ? members[team.value] : null;
  return props.agents.filter(
    (a) =>
      (!q || a.display_name.toLowerCase().includes(q) || a.sip_username.toLowerCase().includes(q)) &&
      (!team.value || (inTeam?.has(a.id) ?? false)),
  );
});

const groups = computed(() =>
  GROUPS.map((g) => {
    const all = filtered.value.filter((a) => groupOf(a) === g.key);
    return { ...g, total: all.length, shown: expanded[g.key] ? all : all.slice(0, PER_GROUP) };
  }).filter((g) => g.total > 0),
);
const liveCount = computed(() => props.agents.filter((a) => groupOf(a) !== "offline").length);
</script>

<template>
  <Panel pad>
    <template #title>Agents</template>
    <template #actions>
      <span class="text-light">{{ liveCount }} online of {{ agents.length }}</span>
    </template>
    <div class="vstack">
      <div class="hstack gap-2">
        <input v-model="search" type="search" placeholder="Search agents" aria-label="Search agents" />
        <select v-model="team" aria-label="Filter by team">
          <option value="">All teams</option>
          <option v-for="t in teams" :key="t" :value="t">{{ t }}</option>
        </select>
      </div>
      <div v-if="!groups.length" class="text-lighter">No agents</div>
      <div v-for="g in groups" :key="g.key" class="vstack gap-2">
        <small class="text-light">{{ g.label }} · {{ g.total }}</small>
        <div v-for="a in g.shown" :key="a.id" class="hstack gap-2">
          <SignalDot :state="g.key" hide-label size="sm" />
          <span class="truncate">{{ a.display_name || a.sip_username }}</span>
          <small class="text-lighter mono">{{ a.sip_username }}</small>
        </div>
        <div v-if="g.total > g.shown.length">
          <button type="button" class="ghost" @click="expanded[g.key] = true">Show all {{ g.total }}</button>
        </div>
      </div>
    </div>
  </Panel>
</template>

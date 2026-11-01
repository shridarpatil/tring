<script setup lang="ts">
// AgentBoard — agents grouped by live state, with a name search and a team
// filter. Each group shows its first 8 until "Show all N" is pressed. An on-call
// agent's row ticks its call time (from the live calls + the page clock).
import { computed, reactive, ref } from "vue";
import type { Agent, Call, QueueDepth } from "../../api/types";
import { agentTeams } from "../../lib/liveDashboard";
import { Panel, SignalDot } from "../ui";

// teams carries each team's member SIP usernames (queues[].agents).
const props = defineProps<{ agents: Agent[]; teams: QueueDepth[]; calls: Call[]; nowMs: number }>();

const PER_GROUP = 8;
const GROUPS = [
  { key: "on_call", label: "ON CALL" },
  { key: "online", label: "AVAILABLE" },
  { key: "away", label: "AWAY" },
  { key: "offline", label: "OFFLINE" },
] as const;
type GroupKey = (typeof GROUPS)[number]["key"];

const search = ref("");
const team = ref("");
const expanded = reactive<Record<string, boolean>>({});

const teamNames = computed(() => props.teams.map((t) => t.team).sort((a, b) => a.localeCompare(b)));
const teamsOf = computed(() => agentTeams(props.teams));

const groupOf = (a: Agent): GroupKey =>
  a.status === "on_call" || a.status === "online" || a.status === "away" ? a.status : "offline";

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase();
  const inTeam = team.value ? new Set(props.teams.find((t) => t.team === team.value)?.agents ?? []) : null;
  return props.agents.filter(
    (a) =>
      (!q || a.display_name.toLowerCase().includes(q) || a.sip_username.toLowerCase().includes(q)) &&
      (!inTeam || inTeam.has(a.sip_username)),
  );
});

const groups = computed(() =>
  GROUPS.map((g) => {
    const all = filtered.value.filter((a) => groupOf(a) === g.key);
    return { ...g, total: all.length, shown: expanded[g.key] ? all : all.slice(0, PER_GROUP) };
  }).filter((g) => g.total > 0),
);
const liveCount = computed(() => props.agents.filter((a) => a.status === "on_call" || a.status === "online").length);
const awayCount = computed(() => props.agents.filter((a) => a.status === "away").length);

// An agent's current call, matched from the live calls by agent_id.
const callByAgent = computed(() => {
  const m = new Map<string, Call>();
  for (const c of props.calls) if (c.agent_id) m.set(c.agent_id, c);
  return m;
});
// "on call · 03:12": talk time once answered, else time since the call started.
function onCallLabel(a: Agent): string {
  const c = callByAgent.value.get(a.id);
  if (!c) return "on call";
  const s = Math.max(0, Math.floor((props.nowMs - Date.parse(c.answered_at ?? c.started_at)) / 1000));
  return `on call · ${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}
</script>

<template>
  <Panel pad>
    <template #title>Agents</template>
    <template #actions>
      <small class="text-light">{{ liveCount }} live · {{ awayCount }} away</small>
    </template>
    <div class="vstack">
      <!-- One row: the search takes 2/3, the team filter 1/3 (Oat grid). -->
      <div class="row">
        <input v-model="search" class="col-8" type="search" placeholder="Search agents" aria-label="Search agents" />
        <select v-model="team" class="col-4" aria-label="Filter by team">
          <option value="">All teams</option>
          <option v-for="t in teamNames" :key="t" :value="t">{{ t }}</option>
        </select>
      </div>
      <div v-if="!groups.length" class="text-lighter"><small>No agents</small></div>
      <table v-else>
        <template v-for="g in groups" :key="g.key">
          <thead>
            <tr>
              <th colspan="2">{{ g.label }} · {{ g.total }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in g.shown" :key="a.id">
              <td>
                <span class="hstack gap-2">
                  <SignalDot :state="g.key" hide-label size="sm" />
                  <small>{{ a.display_name || a.sip_username }}</small>
                  <small v-if="g.key === 'on_call'" class="text-light mono">{{ onCallLabel(a) }}</small>
                </span>
              </td>
              <td class="align-right">
                <span v-if="teamsOf.get(a.sip_username)?.length" class="hstack gap-1 justify-end">
                  <span v-for="tm in teamsOf.get(a.sip_username)" :key="tm" class="badge" data-variant="secondary">{{ tm }}</span>
                </span>
                <small v-if="!teamsOf.get(a.sip_username)?.length" class="text-lighter">—</small>
              </td>
            </tr>
            <tr v-if="g.total > g.shown.length">
              <td colspan="2">
                <button type="button" class="ghost small" @click="expanded[g.key] = true">Show all {{ g.total }}</button>
              </td>
            </tr>
          </tbody>
        </template>
      </table>
    </div>
  </Panel>
</template>

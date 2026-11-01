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
  <Panel class="agent-board">
    <template #title>Agents</template>
    <template #actions>
      <span class="ab-sum">{{ liveCount }} live · {{ awayCount }} away</span>
    </template>
    <div class="agent-tools">
      <input v-model="search" type="search" placeholder="Search agents…" aria-label="Search agents" />
      <select v-model="team" aria-label="Filter by team">
        <option value="">All teams</option>
        <option v-for="t in teamNames" :key="t" :value="t">{{ t }}</option>
      </select>
    </div>
    <div class="ab-scroll">
      <div v-if="!groups.length" class="ab-empty">No agents</div>
      <template v-for="g in groups" :key="g.key">
        <div class="grp">{{ g.label }} · {{ g.total }}</div>
        <div v-for="a in g.shown" :key="a.id" class="agent">
          <SignalDot :state="g.key" hide-label size="sm" />
          <span class="ab-name">
            {{ a.display_name || a.sip_username }}
            <span v-if="g.key === 'on_call'" class="ab-meta mono">{{ onCallLabel(a) }}</span>
          </span>
          <span class="ab-team">{{ teamsOf.get(a.sip_username)?.join(", ") || "—" }}</span>
        </div>
        <button v-if="g.total > g.shown.length" type="button" class="more" @click="expanded[g.key] = true">
          Show all {{ g.total }}
        </button>
      </template>
    </div>
  </Panel>
</template>

<style scoped>
/* Sizes follow the live-dashboard mock; colours are theme tokens. */
.agent-board :deep(.panel-head) { height: auto; padding: 12px 16px; }
.agent-board :deep(.panel-title) { font-size: 14px; font-weight: 600; line-height: 1.45; }
.ab-sum { font-size: 13px; line-height: 1.45; color: var(--muted-foreground); }
.agent-tools { display: flex; gap: 8px; padding: 8px 16px; font-size: 14px; line-height: 1.45; border-bottom: 1px solid var(--border); }
.agent-tools input,
.agent-tools select {
  font: inherit;
  line-height: 1.45;
  height: auto;
  width: auto;
  margin: 0;
  color: inherit;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius-medium);
  padding: 4px 8px;
}
.agent-tools input { flex: 1; min-width: 0; }
.ab-scroll { max-height: 360px; overflow: auto; }
.ab-empty { padding: 16px; font-size: 12px; color: var(--faint-foreground); }
.grp {
  font-size: 12px;
  line-height: 1.45;
  color: var(--muted-foreground);
  padding: 8px 16px 4px;
  background: var(--secondary);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.agent {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 16px;
  font-size: 14px;
  line-height: 1.45;
  border-bottom: 1px solid var(--border);
}
.agent:last-child { border-bottom: 0; }
.ab-name { flex: 1; min-width: 0; }
.ab-meta { margin-left: 6px; font-size: 12px; color: var(--muted-foreground); }
.ab-team { font-size: 12px; color: var(--muted-foreground); text-align: right; }
.more {
  display: block;
  width: 100%;
  margin: 0;
  font: inherit;
  font-size: 14px;
  line-height: 1.45;
  background: transparent;
  border: 0;
  border-radius: 0;
  border-bottom: 1px solid var(--border);
  color: var(--primary);
  padding: 6px 16px;
  text-align: left;
  cursor: pointer;
}
.more:hover { background: var(--secondary); }
</style>

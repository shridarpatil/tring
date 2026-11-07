<script setup lang="ts">
import { computed } from "vue";
import { UsersRound } from "lucide-vue-next";
import { auth } from "../stores/auth";
import { canManageTeam, canSuperviseCall, dashboardScope } from "../lib/roles";
import type { Call, QueueDepth } from "../api/types";
import { Toggle, EmptyState } from "../components/ui";
import DashboardBoard from "../components/dashboard/DashboardBoard.vue";
import { useAgentWidget } from "../composables/useAgentWidget";
import { useDashboardStream } from "../composables/useDashboardStream";
import { useScopedDashboard } from "../composables/useScopedDashboard";

const me = computed(() => auth.state.user);
const { available, savingAvail, setAvailable } = useAgentWidget();

// Supervisors watch the whole tenant over the live stream; agents and team
// managers poll their role-scoped snapshot. A role change needs a new login,
// so the source is picked once.
const scope = me.value ? dashboardScope(me.value) : "own";
const { calls, agents, queues, stats, ready } = scope === "all" ? useDashboardStream() : useScopedDashboard();
const noTeams = computed(() => scope !== "all" && !(me.value?.teams ?? []).length);
const canSupervise = (c: Call) => !!me.value && canSuperviseCall(me.value, c);
// Only teams the user may open get a link; the rest show none.
function teamLink(t: QueueDepth) {
  return me.value && canManageTeam(me.value, t.team) ? { name: "my-team", params: { name: t.team } } : null;
}
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">Dashboard</h1>
      <Toggle
        :model-value="available"
        :disabled="savingAvail"
        label="Available for calls"
        @update:model-value="setAvailable"
      />
    </div>

    <div v-if="noTeams" class="card">
      <EmptyState :icon="UsersRound" title="You are not on any team." hint="Ask an admin to add you to one." />
    </div>

    <!-- Personal scope: the trend strip's unanswered count is the user's missed calls. -->
    <DashboardBoard
      :calls="calls"
      :agents="agents"
      :queues="queues"
      :stats="stats"
      :ready="ready"
      :show-alerts="scope !== 'own'"
      :can-supervise="canSupervise"
      :team-link="teamLink"
      :abandoned-label="scope === 'all' ? undefined : 'Missed'"
    />
  </div>
</template>

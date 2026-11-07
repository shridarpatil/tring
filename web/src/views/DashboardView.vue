<script setup lang="ts">
import { useDashboardStream } from "../composables/useDashboardStream";
import DashboardBoard from "../components/dashboard/DashboardBoard.vue";

// One SSE stream backs every panel (live calls, agents, teams, stats).
const { calls, agents, queues, stats, ready } = useDashboardStream();
</script>

<template>
  <div class="view">
    <h1 class="page-title">Dashboard</h1>
    <DashboardBoard :calls="calls" :agents="agents" :queues="queues" :stats="stats" :ready="ready" />
  </div>
</template>

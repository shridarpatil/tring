<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { canManageTeam, canSuperviseCall, dashboardScope } from "../lib/roles";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import { mountAgentWidget } from "../lib/widget";
import type { Call, Paginated, QueueDepth } from "../api/types";
import { Toggle, CallsTable, Pagination } from "../components/ui";
import DashboardBoard from "../components/dashboard/DashboardBoard.vue";
import { useDashboardStream } from "../composables/useDashboardStream";
import { useScopedDashboard } from "../composables/useScopedDashboard";

const me = computed(() => auth.state.user);

// --- dashboard --------------------------------------------------------------
// Supervisors watch the whole tenant over the live stream; agents and team
// managers poll their role-scoped snapshot. A role change needs a new login,
// so the source is picked once.
const scope = me.value ? dashboardScope(me.value) : "own";
const { calls: liveCalls, agents, queues, stats, ready } = scope === "all" ? useDashboardStream() : useScopedDashboard();
const canSupervise = (c: Call) => !!me.value && canSuperviseCall(me.value, c);
// Only teams the user may open get a link; the rest show none.
function teamLink(t: QueueDepth) {
  return me.value && canManageTeam(me.value, t.team) ? { name: "my-team", params: { name: t.team } } : null;
}

// --- availability -----------------------------------------------------------
// No GET for own availability: the widget's Tring instance carries it and
// emits `availability` on every change, so the toggle mirrors that.
const available = ref(true);
const savingAvail = ref(false);
let offAvail: (() => void) | undefined;
// The widget mount is async: a late resolve must not register a listener (or
// touch state) after the view is gone.
let unmounted = false;

async function setAvailable(v: boolean) {
  const prev = available.value;
  available.value = v;
  savingAvail.value = true;
  try {
    await api.post("/v1/me/availability", { available: v });
  } catch (e) {
    available.value = prev;
    toast.error(errMessage(e, "could not change availability"));
  } finally {
    savingAvail.value = false;
  }
}

// --- recent calls -----------------------------------------------------------
// The dashboard shows only live calls; agents also need their own history.
const PAGE = 10;
const calls = ref<Call[]>([]);
const total = ref(0);
const offset = ref(0);
const loadingCalls = ref(true);
async function loadCalls() {
  loadingCalls.value = true;
  try {
    const res = await api.get<Paginated<Call>>(`/v1/me/calls?limit=${PAGE}&offset=${offset.value}`);
    calls.value = res.items;
    total.value = res.total;
  } catch (e) {
    toast.error(errMessage(e, "could not load calls"));
  } finally {
    loadingCalls.value = false;
  }
}
const page = computed(() => Math.floor(offset.value / PAGE) + 1);
function setPage(n: number) {
  offset.value = (n - 1) * PAGE;
  loadCalls();
}

onMounted(() => {
  loadCalls();
  // The floating panel speaks for itself; only a failure to load it is news.
  mountAgentWidget()
    .then((w) => {
      if (unmounted) return;
      available.value = w.available;
      offAvail = w.on("availability", (e) => (available.value = e.available));
    })
    .catch((e) => !unmounted && toast.error(`Call panel unavailable: ${errMessage(e, "could not load")}`));
});
onUnmounted(() => {
  unmounted = true;
  offAvail?.();
});
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">{{ me?.display_name || "Dashboard" }}</h1>
      <Toggle
        :model-value="available"
        :disabled="savingAvail"
        label="Available for calls"
        @update:model-value="setAvailable"
      />
    </div>

    <DashboardBoard
      :calls="liveCalls"
      :agents="agents"
      :queues="queues"
      :stats="stats"
      :ready="ready"
      :show-alerts="scope !== 'own'"
      :can-supervise="canSupervise"
      :team-link="teamLink"
    />

    <h2>My recent calls</h2>
    <div class="card" style="padding: 0">
      <CallsTable :calls="calls" :loading="loadingCalls" hide-agent />
      <Pagination :page="page" :page-size="PAGE" :total="total" label="calls" @update:page="setPage" />
    </div>
  </div>
</template>

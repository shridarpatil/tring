<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { Phone } from "lucide-vue-next";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { canManageTeam } from "../lib/roles";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import { mountAgentWidget } from "../lib/widget";
import { fmtTalk } from "../lib/me";
import type { Call, Paginated, QueueDepth } from "../api/types";
import { Toggle, Button, KpiCard, CallsTable, Pagination } from "../components/ui";
import TeamBoard from "../components/dashboard/TeamBoard.vue";

interface Stats {
  taken: number;
  missed: number;
  talk_seconds: number;
  avg_talk_seconds: number;
}
// /v1/my-teams items carry the dashboard's QueueDepth under `queue`.
interface MyTeam {
  name: string;
  queue: QueueDepth;
}

const me = computed(() => auth.state.user);

// --- availability -----------------------------------------------------------
// No GET for own availability: the widget's Tring instance carries it and
// emits `availability` on every change, so the toggle mirrors that.
const available = ref(true);
const widgetReady = ref(false);
const widgetError = ref("");
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

// --- stats ------------------------------------------------------------------
const range = ref<"today" | "7d">("today");
const stats = ref<Stats | null>(null);
async function loadStats() {
  try {
    stats.value = await api.get<Stats>(`/v1/me/stats?range=${range.value}`);
  } catch (e) {
    toast.error(errMessage(e, "could not load stats"));
  }
}
watch(range, loadStats);
const rangeLabel = computed(() => (range.value === "today" ? "today" : "last 7 days"));

// --- my teams (polled) ------------------------------------------------------
const teams = ref<MyTeam[]>([]);
const now = ref(Date.now());
let poll: ReturnType<typeof setInterval> | undefined;
async function loadTeams() {
  try {
    teams.value = await api.get<MyTeam[]>("/v1/my-teams");
  } catch {
    /* transient; the next tick retries */
  }
  now.value = Date.now();
}
const queues = computed(() => teams.value.map((t) => t.queue));
// Only teams the user may open get a link; the rest show none.
function teamLink(t: QueueDepth) {
  return me.value && canManageTeam(me.value, t.team) ? { name: "my-team", params: { name: t.team } } : null;
}

// --- recent calls -----------------------------------------------------------
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
  loadStats();
  loadTeams();
  loadCalls();
  poll = setInterval(loadTeams, 5000);
  mountAgentWidget()
    .then((w) => {
      if (unmounted) return;
      available.value = w.available;
      offAvail = w.on("availability", (e) => (available.value = e.available));
      widgetReady.value = true;
    })
    .catch((e) => !unmounted && (widgetError.value = errMessage(e, "call panel unavailable")));
});
onUnmounted(() => {
  unmounted = true;
  clearInterval(poll);
  offAvail?.();
});
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">{{ me?.display_name || "My calls" }}</h1>
      <Toggle
        :model-value="available"
        :disabled="savingAvail"
        label="Available for calls"
        @update:model-value="setAvailable"
      />
    </div>

    <div class="card hstack">
      <Phone :size="18" />
      <span v-if="widgetError" class="text-light">Call panel unavailable: {{ widgetError }}</span>
      <span v-else-if="!widgetReady" class="text-light">Connecting call panel…</span>
      <span v-else class="text-light">Call panel ready. Use the floating panel to answer and place calls.</span>
    </div>

    <div class="row-between">
      <h2>My stats</h2>
      <div class="hstack">
        <Button size="sm" :variant="range === 'today' ? 'primary' : 'secondary'" @click="range = 'today'">Today</Button>
        <Button size="sm" :variant="range === '7d' ? 'primary' : 'secondary'" @click="range = '7d'">7 days</Button>
      </div>
    </div>
    <div class="row">
      <KpiCard class="col-3" :value="stats?.taken ?? 0" label="Taken" :sub="rangeLabel" />
      <KpiCard class="col-3" :value="stats?.missed ?? 0" label="Missed" :sub="rangeLabel" :tone="(stats?.missed ?? 0) > 0 ? 'danger' : undefined" />
      <KpiCard class="col-3" :value="fmtTalk(stats?.talk_seconds ?? 0)" label="Talk time" :sub="rangeLabel" />
      <KpiCard class="col-3" :value="fmtTalk(stats?.avg_talk_seconds ?? 0)" label="Avg talk" sub="per call" />
    </div>

    <div class="row">
      <div class="col-7">
        <div class="card" style="padding: 0">
          <CallsTable :calls="calls" :loading="loadingCalls" hide-agent />
          <Pagination :page="page" :page-size="PAGE" :total="total" label="calls" @update:page="setPage" />
        </div>
      </div>
      <div class="col-5">
        <p v-if="!teams.length" class="card text-lighter">You are not on any team.</p>
        <TeamBoard v-else :teams="queues" :now-ms="now" :team-link="teamLink" card-class="col-6" />
      </div>
    </div>
  </div>
</template>

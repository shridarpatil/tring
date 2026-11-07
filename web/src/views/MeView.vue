<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { RouterLink } from "vue-router";
import { Phone } from "lucide-vue-next";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { canManageTeam } from "../lib/roles";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import { callStatusTone } from "../lib/status";
import { mountAgentWidget } from "../lib/widget";
import { fmtTalk, longestWaitSeconds, memberTone, queueSummary } from "../lib/me";
import type { Call, Paginated } from "../api/types";
import { Toggle, Badge, Button } from "../components/ui";

interface Stats {
  taken: number;
  missed: number;
  talk_seconds: number;
  avg_talk_seconds: number;
}
interface Member {
  user_id: string;
  sip_username: string;
  display_name?: string;
  is_manager: boolean;
  status: string;
}
interface MyTeam {
  name: string;
  is_manager: boolean;
  queue: { waiting: number; waiting_since: string[] | null };
  members: Member[];
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
function canOpenTeam(name: string): boolean {
  return !!me.value && canManageTeam(me.value, name);
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
function pageCalls(delta: number) {
  offset.value = Math.max(0, offset.value + delta * PAGE);
  loadCalls();
}

// Recordings sit behind auth: fetch as a blob, bind to an inline player.
const recUrl = ref<Record<string, string>>({});
async function playRecording(c: Call) {
  if (!c.recording_url || recUrl.value[c.id]) return;
  const token = auth.token();
  const res = await fetch(c.recording_url, { headers: token ? { Authorization: `Bearer ${token}` } : {} });
  if (!res.ok) {
    toast.error("recording not available yet");
    return;
  }
  recUrl.value = { ...recUrl.value, [c.id]: URL.createObjectURL(await res.blob()) };
}
function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "—";
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
  Object.values(recUrl.value).forEach((u) => URL.revokeObjectURL(u));
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

    <div class="card vstack">
      <div class="row-between">
        <h2>My stats</h2>
        <div class="hstack">
          <Button size="sm" :variant="range === 'today' ? 'primary' : 'secondary'" @click="range = 'today'">Today</Button>
          <Button size="sm" :variant="range === '7d' ? 'primary' : 'secondary'" @click="range = '7d'">7 days</Button>
        </div>
      </div>
      <div class="hstack">
        <div style="flex: 1">
          <div class="trend-num">{{ stats?.taken ?? 0 }}</div>
          <small class="text-light">Taken</small>
        </div>
        <div style="flex: 1">
          <div class="trend-num">{{ stats?.missed ?? 0 }}</div>
          <small class="text-light">Missed</small>
        </div>
        <div style="flex: 1">
          <div class="trend-num">{{ fmtTalk(stats?.talk_seconds ?? 0) }}</div>
          <small class="text-light">Talk time</small>
        </div>
        <div style="flex: 1">
          <div class="trend-num">{{ fmtTalk(stats?.avg_talk_seconds ?? 0) }}</div>
          <small class="text-light">Avg talk</small>
        </div>
      </div>
    </div>

    <div class="card vstack">
      <h2>My teams</h2>
      <p v-if="teams.length === 0" class="text-lighter">You are not on any team.</p>
      <div v-for="t in teams" :key="t.name" class="vstack">
        <div class="row-between">
          <strong>
            <RouterLink v-if="canOpenTeam(t.name)" :to="{ name: 'my-team', params: { name: t.name } }">{{ t.name }}</RouterLink>
            <template v-else>{{ t.name }}</template>
          </strong>
          <small class="text-light">{{ queueSummary(t.queue.waiting, longestWaitSeconds(t.queue.waiting_since, now)) }}</small>
        </div>
        <div class="hstack">
          <Badge v-for="m in t.members" :key="m.user_id" :tone="memberTone(m.status)">
            {{ m.display_name || m.sip_username }} · {{ m.status }}
          </Badge>
        </div>
      </div>
    </div>

    <div class="card" style="padding: 0">
      <div class="table">
        <table>
          <thead>
            <tr>
              <th>From</th>
              <th>To</th>
              <th>Status</th>
              <th>Started</th>
              <th class="align-right">Dur</th>
              <th>Recording</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in calls" :key="c.id">
              <td class="mono">{{ c.client_ref || c.from_number }}</td>
              <td class="mono">{{ c.to_number }}</td>
              <td><Badge :tone="callStatusTone(c.status)">{{ c.status }}</Badge></td>
              <td class="text-light" style="white-space: nowrap">{{ fmt(c.started_at) }}</td>
              <td class="align-right mono">{{ c.duration_seconds != null ? fmtTalk(c.duration_seconds) : "—" }}</td>
              <td>
                <audio v-if="recUrl[c.id]" :src="recUrl[c.id]" controls autoplay style="height: 2rem"></audio>
                <button v-else-if="c.recording_url" type="button" class="plain" style="color: var(--primary)" @click="playRecording(c)">Play</button>
                <span v-else class="text-lighter">—</span>
              </td>
            </tr>
            <tr v-if="!loadingCalls && calls.length === 0">
              <td colspan="6" class="align-center text-lighter">No calls yet</td>
            </tr>
            <tr v-if="loadingCalls">
              <td colspan="6" class="align-center text-lighter">Loading…</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="total > PAGE" class="row-between" style="padding: 0.75rem">
        <small class="text-light">{{ offset + 1 }}–{{ Math.min(offset + PAGE, total) }} of {{ total }}</small>
        <div class="hstack">
          <Button size="sm" variant="secondary" :disabled="offset === 0" @click="pageCalls(-1)">Previous</Button>
          <Button size="sm" variant="secondary" :disabled="offset + PAGE >= total" @click="pageCalls(1)">Next</Button>
        </div>
      </div>
    </div>
  </div>
</template>

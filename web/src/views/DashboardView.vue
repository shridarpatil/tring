<script setup lang="ts">
import { ref, computed } from "vue";
import { usePoll } from "../composables/usePoll";
import { useDashboardStream } from "../composables/useDashboardStream";
import type { Call } from "../api/types";
import { Panel, LevelMeter } from "../components/ui";
import MiniBarChart from "../components/MiniBarChart.vue";
import DashAlerts from "../components/dashboard/DashAlerts.vue";
import TeamBoard from "../components/dashboard/TeamBoard.vue";
import AgentBoard from "../components/dashboard/AgentBoard.vue";
import {
  abandonedPct,
  agentsLiveRequired,
  sharedSla,
  breachingCount,
  buildAlerts,
  callSlaBadge,
  filterLiveCalls,
  fmtMMSS,
  type CallFilter,
  isWaitingCall,
  longestWait,
  sortLiveCalls,
} from "../lib/liveDashboard";
import { api } from "../api/client";
import { SupervisorSession, type SuperviseMode } from "../lib/supervisor";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";

// One SSE stream backs every panel (live calls, agents, teams, stats).
const { calls, agents, queues, stats, ready } = useDashboardStream();

// Ticking clock: drives live call/agent durations between server updates. Durations
// are derived from started_at/answered_at + this clock, so they tick every second
// AND stay exactly correct — no drift, no waiting on the server to see them move.
const now = ref(Date.now());

// ── live durations ────────────────────────────────────────────────────
function fmtDur(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const m = Math.floor(s / 60);
  return String(m).padStart(2, "0") + ":" + String(s % 60).padStart(2, "0");
}
// Talk time once answered; otherwise time spent ringing.
function callSeconds(c: Call): number {
  const base = c.answered_at ?? c.started_at;
  return (now.value - new Date(base).getTime()) / 1000;
}

// ── KPI derivations ───────────────────────────────────────────────────
const teams = computed(() => queues.value.queues);
const alerts = computed(() => buildAlerts(teams.value, now.value));
const hasTalking = computed(() => calls.value.some((c) => c.status === "answered"));
const waitingNow = computed(() => teams.value.reduce((n, t) => n + t.waiting_since.length, 0));
const longestNow = computed(() => Math.max(0, ...teams.value.map((t) => longestWait(t, now.value))));
const breachingNow = computed(() => teams.value.reduce((n, t) => n + breachingCount(t, now.value), 0));
const staffing = computed(() => agentsLiveRequired(teams.value));
const slaLimit = computed(() => sharedSla(teams.value));
const abandonedShare = computed(() => abandonedPct(stats.value?.abandoned_today ?? 0, stats.value?.calls_today ?? 0));

// ── live calls: waiting callers first, filterable ─────────────────────
const callFilters: { key: CallFilter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "waiting", label: "Waiting" },
  { key: "talking", label: "Talking" },
];
const callFilter = ref<CallFilter>("all");
// The supervised call stays listed under any filter so its controls stay reachable.
const shownCalls = computed(() => filterLiveCalls(sortLiveCalls(calls.value), callFilter.value, active.value?.callId));

// ── live-call row presentation ────────────────────────────────────────
function sourceIcon(c: Call): string {
  if (c.source === "web") return "🌐";
  if (c.source === "whatsapp") return "💬";
  return "☎";
}
function callerLabel(c: Call): string {
  if (c.source === "web") return "Web visitor";
  return c.from_number || "Unknown";
}
function destLabel(c: Call): string {
  // A ring in progress (including a transfer re-ring on an answered call) shows who
  // it's ringing (ring_target); otherwise the answering agent, then the flow/number.
  if (c.phase === "ringing" && c.ring_target) return c.ring_target;
  return c.agent_name || c.ring_target || c.to_number || "—";
}

// Live-call status badge. Pre-terminal states the operator cares about: ringing
// (an agent is being rung — destLabel shows who), talking (answered), and in IVR
// (caller in the menu, nobody being rung yet). An active ring is checked first so a
// transfer re-ring on an in-progress call reads "ringing" until the new agent picks
// up, rather than staying "talking" on the agent who handed it off.
function callBadge(c: Call): { text: string; cls: string } {
  if (c.phase === "ringing") return { text: "ringing", cls: "accent" };
  if (c.status === "answered") return { text: "talking", cls: "live" };
  if (c.phase === "ivr") return { text: "in IVR", cls: "secondary" };
  return { text: "ringing", cls: "accent" };
}

// ── supervisor (Listen/Whisper/Barge) ─────────────────────────────────
// v1 simplification: only ONE supervise session at a time across the whole
// dashboard (a single `active` ref, not one per row).
const active = ref<{ callId: string; mode: SuperviseMode; session: SupervisorSession } | null>(null);

async function startSupervise(c: Call): Promise<void> {
  // Switching targets mid-session must hang up the previous one — otherwise
  // its WebSocket/PeerConnection stay open and, in whisper/barge, the
  // operator keeps transmitting into the old call's room.
  if (active.value) {
    active.value.session.hangup();
    active.value = null;
  }
  try {
    const creds = await api.supervise(c.id);
    const session = new SupervisorSession(creds.gateway_url, creds.room, creds.token, creds.ice_servers || []);
    session.onClose = (reason) => {
      // Only clear if this is still the active session (guard against races).
      if (active.value?.session === session) active.value = null;
      if (reason) toast.error(reason);
    };
    await session.connect("monitor");
    active.value = { callId: c.id, mode: "monitor", session };
  } catch (e) {
    toast.error(errMessage(e, "Could not start supervising this call"));
  }
}
function setMode(mode: SuperviseMode): void {
  if (!active.value) return;
  active.value.session.setMode(mode);
  active.value.mode = mode;
}
function stopSupervise(): void {
  active.value?.session.hangup();
  active.value = null;
}

// Clock tick for live call/agent durations — pure local UI, no network.
usePoll(() => (now.value = Date.now()), 1000);
</script>

<template>
  <div class="view">
    <h1 class="page-title">Dashboard</h1>
    <div v-if="!ready" class="text-lighter">Loading…</div>

    <template v-else>
      <DashAlerts :alerts="alerts" :now-ms="now" />

      <!-- Trend strip: today so far -->
      <!-- hstack only adds wrapping: the chart drops to its own line on narrow screens -->
      <div class="card trend-strip hstack">
        <div class="row" style="flex: 0 0 15rem">
          <div class="col-6">
            <div class="trend-num">{{ stats?.calls_today ?? 0 }}</div>
            <small class="text-light">Calls today</small>
          </div>
          <div class="col-6">
            <div class="trend-num" style="color: var(--success)">{{ stats?.answered_pct ?? 0 }}%</div>
            <small class="text-light">Answered</small>
          </div>
          <div class="col-6 mt-2">
            <div class="trend-num">{{ fmtDur(stats?.avg_talk_seconds ?? 0) }}</div>
            <small class="text-light">Avg talk</small>
          </div>
          <div class="col-6 mt-2">
            <div class="trend-num" :style="{ color: (stats?.sla_breaches_today ?? 0) > 0 ? 'var(--danger)' : undefined }">
              {{ stats?.sla_breaches_today ?? 0 }}
            </div>
            <small class="text-light">SLA breaches</small>
          </div>
        </div>
        <div class="trend-divider"></div>
        <div style="flex: 1 1 auto; min-width: 0">
          <MiniBarChart v-if="stats" :buckets="stats.hourly" />
        </div>
      </div>

      <!-- Headline numbers -->
      <div class="row">
        <div class="card col-2">
          <div class="kpi-num">{{ queues.on_call }}</div>
          <div><small class="text-light">On call</small></div>
          <small class="text-lighter">{{ calls.length }} active incl. IVR/ringing</small>
        </div>
        <div class="card col-2">
          <div class="kpi-num" :style="{ color: waitingNow > 0 ? 'var(--danger)' : undefined }">{{ waitingNow }}</div>
          <div><small class="text-light">Waiting now</small></div>
          <small v-if="waitingNow" class="text-lighter">longest <span style="color: var(--danger)">{{ fmtMMSS(longestNow) }}</span></small>
          <small v-else class="text-lighter">no queue</small>
        </div>
        <div class="card col-2">
          <div class="kpi-num" :style="{ color: (stats?.sla_breaches_today ?? 0) > 0 ? 'var(--danger)' : undefined }">
            {{ stats?.sla_breaches_today ?? 0 }}
          </div>
          <div><small class="text-light">SLA breaches today</small></div>
          <small v-if="breachingNow > 0"><strong style="color: var(--danger)">{{ breachingNow }} breaching now</strong></small>
          <small v-else class="text-lighter">none breaching now</small>
        </div>
        <div class="card col-2">
          <div class="kpi-num" :style="{ color: staffing.live < staffing.min ? 'var(--danger)' : undefined }">
            {{ staffing.live }}<small class="text-lighter"> / {{ staffing.min }}</small>
          </div>
          <div><small class="text-light">Agents live / required</small></div>
          <progress v-if="staffing.min > 0" :value="staffing.live" :max="staffing.min"></progress>
        </div>
        <div class="card col-2">
          <div class="kpi-num">{{ fmtMMSS(stats?.avg_queue_wait_seconds ?? 0) }}</div>
          <div><small class="text-light">Avg queue wait</small></div>
          <small v-if="slaLimit" class="text-lighter">SLA limit {{ fmtMMSS(slaLimit) }}</small>
        </div>
        <div class="card col-2">
          <div class="kpi-num" :style="{ color: (stats?.abandoned_today ?? 0) > 10 ? 'var(--warning)' : undefined }">
            {{ stats?.abandoned_today ?? 0 }}
          </div>
          <div><small class="text-light">Abandoned today</small></div>
          <small v-if="abandonedShare" class="text-lighter">{{ abandonedShare }}% of calls</small>
        </div>
      </div>

      <TeamBoard :teams="teams" :now-ms="now" />

      <!-- Main row: live calls | agents -->
      <div class="dash-main">
        <Panel>
          <template #title>Live calls</template>
          <template #actions>
            <LevelMeter v-if="hasTalking" active />
            <small class="text-light">{{ calls.length }} active</small>
          </template>
          <div class="hstack gap-2 p-4">
            <button
              v-for="f in callFilters"
              :key="f.key"
              type="button"
              class="small"
              :class="{ outline: callFilter !== f.key }"
              :aria-pressed="callFilter === f.key"
              @click="callFilter = f.key"
            >
              {{ f.label }}
            </button>
          </div>
          <div class="scroll-max" style="--max-h: 28rem">
          <div v-if="!shownCalls.length" class="list-empty">
            <small>No live calls</small>
          </div>
          <div v-for="c in shownCalls" :key="c.id" class="live-call-row">
            <span class="source-badge" aria-hidden="true">{{ sourceIcon(c) }}</span>
            <div style="flex: 1 1 auto; min-width: 0">
              <div class="truncate">
                <small>{{ callerLabel(c) }} → <strong>{{ destLabel(c) }}</strong></small>
              </div>
              <div class="text-lighter mono truncate">
                <small>{{ c.to_number }}<template v-if="isWaitingCall(c)"> · waiting in {{ c.queue_team }}</template></small>
              </div>
              <span v-if="callSlaBadge(c, now)" class="badge" :data-variant="callSlaBadge(c, now)!.variant">{{
                callSlaBadge(c, now)!.text
              }}</span>
            </div>
            <span class="badge" :data-variant="callBadge(c).cls" style="flex-shrink: 0">{{ callBadge(c).text }}</span>
            <small class="mono text-light" style="flex-shrink: 0; width: 3rem; text-align: right">{{
              fmtDur(callSeconds(c))
            }}</small>
            <!-- Supervisor controls: any call with a media room (web, phone,
                 WhatsApp; calls placed over ESL have none). Only ONE supervise
                 session is supported at a time (the `active` ref, not per-row state). -->
            <div v-if="c.room_id" style="flex-shrink: 0; display: flex; align-items: center; gap: 0.25rem">
              <span v-if="c.supervised && active?.callId !== c.id" class="text-lighter" :title="'Supervised by ' + c.supervised"
                >🎧 {{ c.supervised }}</span
              >
              <button
                v-if="active?.callId !== c.id"
                type="button"
                class="dash-pill"
                @click="startSupervise(c)"
              >
                Listen
              </button>
              <template v-else>
                <button
                  v-for="m in (['monitor', 'whisper', 'barge'] as const)"
                  :key="m"
                  type="button"
                  class="dash-pill"
                  :class="{ 'dash-pill-active': active.mode === m }"
                  @click="setMode(m)"
                >
                  {{ m }}
                </button>
                <button type="button" class="dash-pill dash-pill-danger" @click="stopSupervise()">
                  Stop
                </button>
              </template>
            </div>
          </div>
          </div>
        </Panel>

        <AgentBoard :agents="agents" :teams="teams" :calls="calls" :now-ms="now" />
      </div>
    </template>
  </div>
</template>

<style scoped>
/* Structural + typographic treatments Oat has no utility for — colours and
   spacing all come from tokens, nothing hardcoded. */
.trend-strip { display: flex; align-items: center; gap: 1.5rem; }
.trend-num { font-size: 1.25rem; font-weight: 600; font-variant-numeric: tabular-nums; }
.trend-label { font-size: var(--text-8); color: var(--muted-foreground); margin-top: 0.25rem; }
.trend-divider { width: 1px; align-self: stretch; background: var(--border); }

.kpi-num { font-size: 1.875rem; font-weight: 600; font-variant-numeric: tabular-nums; }

.dash-main { display: grid; grid-template-columns: 1.6fr 1fr; gap: 1rem; align-items: start; }
@media (max-width: 900px) {
  .dash-main { grid-template-columns: 1fr; }
}

.scroll-max { max-height: var(--max-h); overflow-y: auto; }
.list-empty { padding: 2rem 1.25rem; text-align: center; color: var(--faint-foreground); }

.live-call-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.75rem 1.25rem;
  border-bottom: 1px solid var(--border);
}
.live-call-row:last-child { border-bottom: none; }

.source-badge {
  flex-shrink: 0;
  width: 1.75rem;
  height: 1.75rem;
  border-radius: var(--radius-medium);
  background: var(--secondary);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: var(--text-8);
}

.agent-row {
  display: flex;
  align-items: center;
  gap: 0.625rem;
  padding: 0.5rem 1rem;
  font-size: var(--text-8);
  border-bottom: 1px solid var(--border);
}
.agent-row:last-child { border-bottom: none; }

.dash-pill {
  font-size: var(--text-8);
  padding: 0.125rem 0.5rem;
  border-radius: var(--radius-medium);
  background: var(--secondary);
  text-transform: capitalize;
  transition: background-color var(--transition-fast);
  border: none;
  cursor: pointer;
  color: inherit;
  font-family: inherit;
}
.dash-pill:hover { background: var(--card); }
.dash-pill-active { background: var(--primary); color: var(--primary-foreground); }
.dash-pill-danger { background: var(--color-danger-soft); color: var(--danger); }
</style>

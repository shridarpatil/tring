<script setup lang="ts">
import { ref, computed } from "vue";
import { usePoll } from "../composables/usePoll";
import { useDashboardStream } from "../composables/useDashboardStream";
import type { Call, Agent } from "../api/types";
import { Panel, SignalDot, LevelMeter } from "../components/ui";
import MiniBarChart from "../components/MiniBarChart.vue";
import QueuePressureBar from "../components/QueuePressureBar.vue";
import { api } from "../api/client";
import { SupervisorSession, type SuperviseMode } from "../lib/supervisor";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";

// One SSE stream backs all four panels (live calls, agents, queues, stats).
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
const availableAgents = computed(() => agents.value.filter((a) => a.status === "online").length);
const hasTalking = computed(() => calls.value.some((c) => c.status === "answered"));
const queueMax = computed(() => Math.max(1, ...queues.value.queues.map((q) => q.waiting)));

// An agent's current call (for the on-call timer on the agent board), matched
// from the active-calls list by agent_id.
const callByAgent = computed(() => {
  const m = new Map<string, Call>();
  for (const c of calls.value) if (c.agent_id) m.set(c.agent_id, c);
  return m;
});
function agentStatusLabel(a: Agent): string {
  if (a.status === "on_call") {
    const c = a.id ? callByAgent.value.get(a.id) : undefined;
    return c ? "on call · " + fmtDur(callSeconds(c)) : "on call";
  }
  if (a.status === "online") return "available";
  if (a.status === "away") return "away";
  return "offline";
}
// Sort the board: on-call first, then available, away, offline.
const rank: Record<string, number> = { on_call: 0, online: 1, away: 2, offline: 3, unknown: 4 };
const sortedAgents = computed(() =>
  [...agents.value].sort((a, b) => (rank[a.status ?? "unknown"] ?? 4) - (rank[b.status ?? "unknown"] ?? 4)),
);
const onlineCount = computed(() => agents.value.filter((a) => a.status && a.status !== "offline").length);

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
      <!-- Trend strip: today so far -->
      <div class="card trend-strip">
        <div style="flex-shrink: 0">
          <div class="trend-num">{{ stats?.calls_today ?? 0 }}</div>
          <div class="trend-label">Calls today</div>
        </div>
        <div style="flex-shrink: 0">
          <div class="trend-num" style="color: var(--success)">{{ stats?.answered_pct ?? 0 }}%</div>
          <div class="trend-label">Answered</div>
        </div>
        <div style="flex-shrink: 0">
          <div class="trend-num">{{ fmtDur(stats?.avg_talk_seconds ?? 0) }}</div>
          <div class="trend-label">Avg talk</div>
        </div>
        <div class="trend-divider"></div>
        <div style="flex: 1 1 auto; min-width: 0">
          <MiniBarChart v-if="stats" :buckets="stats.hourly" />
        </div>
      </div>

      <!-- KPI hero row. On-call + waiting aren't here — they already show in the
           Live calls / Queues panels below; these surface outcomes instead. -->
      <div class="grid grid-4">
        <div class="card">
          <div class="kpi-num" :style="{ color: (stats?.abandoned_today ?? 0) > 0 ? 'var(--warning)' : undefined }">
            {{ stats?.abandoned_today ?? 0 }}
          </div>
          <div class="text-light" style="margin-top: 0.25rem">Abandoned today</div>
        </div>

        <div class="card">
          <div class="kpi-num" :style="{ color: (stats?.failed_today ?? 0) > 0 ? 'var(--danger)' : undefined }">
            {{ stats?.failed_today ?? 0 }}
          </div>
          <div class="text-light" style="margin-top: 0.25rem">Failed today</div>
        </div>

        <div class="card">
          <div class="kpi-num">
            {{ availableAgents }}<span class="text-lighter" style="font-size: 1.125rem"> / {{ agents.length }}</span>
          </div>
          <div class="text-light" style="margin-top: 0.25rem">Agents available</div>
        </div>

        <div class="card">
          <div class="kpi-num">{{ fmtDur(stats?.avg_wait_seconds ?? 0) }}</div>
          <div class="text-light" style="margin-top: 0.25rem">Avg wait today</div>
        </div>
      </div>

      <!-- Main row: live calls | queues + agents -->
      <div class="dash-main">
        <Panel>
          <template #title>Live calls</template>
          <template #actions>
            <LevelMeter v-if="hasTalking" active />
            <span class="text-light">{{ calls.length }} active</span>
          </template>
          <div class="scroll-max" style="--max-h: 28rem">
          <div v-if="!calls.length" class="list-empty">
            No live calls
          </div>
          <div v-for="c in calls" :key="c.id" class="live-call-row">
            <span class="source-badge" aria-hidden="true">{{ sourceIcon(c) }}</span>
            <div style="flex: 1 1 auto; min-width: 0">
              <div class="truncate">
                {{ callerLabel(c) }} → <span style="font-weight: 500">{{ destLabel(c) }}</span>
              </div>
              <div class="text-lighter mono truncate">{{ c.to_number }}</div>
            </div>
            <span class="badge" :data-variant="callBadge(c).cls" style="flex-shrink: 0">{{ callBadge(c).text }}</span>
            <span class="mono text-light" style="flex-shrink: 0; width: 3rem; text-align: right">{{
              fmtDur(callSeconds(c))
            }}</span>
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

        <div class="vstack">
          <Panel>
            <template #title>Queues by team</template>
            <template #actions>
              <span class="text-light">{{ queues.total }} waiting</span>
            </template>
            <div class="scroll-max" style="--max-h: 14rem">
            <div v-if="!queues.queues.length" class="list-empty">
              No teams
            </div>
            <QueuePressureBar
              v-for="q in queues.queues"
              :key="q.team"
              :team="q.team"
              :waiting="q.waiting"
              :max="queueMax"
            />
            </div>
          </Panel>

          <Panel>
            <template #title>Agents</template>
            <template #actions>
              <span class="text-light">{{ onlineCount }} online · {{ queues.on_call }} on call</span>
            </template>
            <div class="scroll-max" style="--max-h: 20rem">
            <div v-if="!agents.length" class="list-empty">
              No agents
            </div>
            <div v-for="a in sortedAgents" :key="a.id" class="agent-row">
              <SignalDot :state="(a.status === 'unknown' ? 'offline' : a.status) ?? 'offline'" hide-label size="sm" />
              <span class="truncate" style="flex: 1 1 auto">{{ a.display_name || a.sip_username }}</span>
              <span class="text-light" style="font-variant-numeric: tabular-nums">{{ agentStatusLabel(a) }}</span>
            </div>
            </div>
          </Panel>
        </div>
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

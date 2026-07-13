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
  // While ringing an agent/team, show who it's ringing (ring_target) as the
  // destination; once answered agent_name wins; otherwise the dialed number/flow.
  return c.agent_name || c.ring_target || c.to_number || "—";
}

// Live-call status badge. Three pre-terminal states the operator cares about:
// talking (answered), in IVR (caller in the menu, nobody being rung yet), and
// ringing (an agent/team is being rung — destLabel shows who).
function callBadge(c: Call): { text: string; cls: string } {
  if (c.status === "answered") return { text: "talking", cls: "text-live bg-live-soft" };
  if (c.phase === "ivr") return { text: "in IVR", cls: "text-ink-muted bg-surface-2" };
  return { text: "ringing", cls: "text-accent bg-accent-soft" };
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
  <div class="space-y-4">
    <h1 class="text-xl font-semibold tracking-tight">Dashboard</h1>
    <div v-if="!ready" class="text-ink-faint text-sm">Loading…</div>

    <template v-else>
      <!-- Trend strip: today so far -->
      <div class="rounded-xl bg-surface border border-border px-5 py-3.5 flex items-center gap-6">
        <div class="shrink-0">
          <div class="text-xl font-semibold tabular-nums">{{ stats?.calls_today ?? 0 }}</div>
          <div class="text-[11px] text-ink-muted mt-1">Calls today</div>
        </div>
        <div class="shrink-0">
          <div class="text-xl font-semibold tabular-nums text-success">{{ stats?.answered_pct ?? 0 }}%</div>
          <div class="text-[11px] text-ink-muted mt-1">Answered</div>
        </div>
        <div class="shrink-0">
          <div class="text-xl font-semibold tabular-nums">{{ fmtDur(stats?.avg_talk_seconds ?? 0) }}</div>
          <div class="text-[11px] text-ink-muted mt-1">Avg talk</div>
        </div>
        <div class="w-px self-stretch bg-border"></div>
        <div class="flex-1 min-w-0">
          <MiniBarChart v-if="stats" :buckets="stats.hourly" />
        </div>
      </div>

      <!-- KPI hero row. On-call + waiting aren't here — they already show in the
           Live calls / Queues panels below; these surface outcomes instead. -->
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <div class="rounded-xl bg-surface border border-border p-5">
          <div class="text-3xl font-semibold tabular-nums" :class="(stats?.abandoned_today ?? 0) > 0 ? 'text-warning' : ''">
            {{ stats?.abandoned_today ?? 0 }}
          </div>
          <div class="text-sm text-ink-muted mt-1">Abandoned today</div>
        </div>

        <div class="rounded-xl bg-surface border border-border p-5">
          <div class="text-3xl font-semibold tabular-nums" :class="(stats?.failed_today ?? 0) > 0 ? 'text-danger' : ''">
            {{ stats?.failed_today ?? 0 }}
          </div>
          <div class="text-sm text-ink-muted mt-1">Failed today</div>
        </div>

        <div class="rounded-xl bg-surface border border-border p-5">
          <div class="text-3xl font-semibold tabular-nums">
            {{ availableAgents }}<span class="text-lg text-ink-faint"> / {{ agents.length }}</span>
          </div>
          <div class="text-sm text-ink-muted mt-1">Agents available</div>
        </div>

        <div class="rounded-xl bg-surface border border-border p-5">
          <div class="text-3xl font-semibold tabular-nums">{{ fmtDur(stats?.avg_wait_seconds ?? 0) }}</div>
          <div class="text-sm text-ink-muted mt-1">Avg wait today</div>
        </div>
      </div>

      <!-- Main row: live calls | queues + agents -->
      <div class="grid grid-cols-1 lg:grid-cols-[1.6fr_1fr] gap-4 items-start">
        <Panel>
          <template #title>Live calls</template>
          <template #actions>
            <LevelMeter v-if="hasTalking" active />
            <span class="text-xs text-ink-muted">{{ calls.length }} active</span>
          </template>
          <div class="max-h-[28rem] overflow-y-auto">
          <div v-if="!calls.length" class="px-5 py-8 text-sm text-ink-faint text-center">
            No live calls
          </div>
          <div
            v-for="c in calls"
            :key="c.id"
            class="flex items-center gap-3 px-5 py-3 border-b border-border last:border-b-0"
          >
            <span
              class="shrink-0 w-7 h-7 rounded-md bg-surface-2 flex items-center justify-center text-xs"
              aria-hidden="true"
              >{{ sourceIcon(c) }}</span
            >
            <div class="flex-1 min-w-0">
              <div class="text-sm truncate">
                {{ callerLabel(c) }} → <span class="font-medium">{{ destLabel(c) }}</span>
              </div>
              <div class="text-[11.5px] text-ink-faint font-mono truncate">{{ c.to_number }}</div>
            </div>
            <span
              class="text-[10.5px] px-2 py-0.5 rounded-full font-semibold shrink-0"
              :class="callBadge(c).cls"
              >{{ callBadge(c).text }}</span
            >
            <span class="tabular-nums text-sm text-ink-muted shrink-0 w-12 text-right">{{
              fmtDur(callSeconds(c))
            }}</span>
            <!-- Supervisor controls: web calls only. Only ONE supervise session
                 is supported at a time (the `active` ref, not per-row state). -->
            <div v-if="c.source === 'web'" class="shrink-0 flex items-center gap-1">
              <span v-if="c.supervised && active?.callId !== c.id" class="text-[10.5px] text-ink-faint" :title="'Supervised by ' + c.supervised"
                >🎧 {{ c.supervised }}</span
              >
              <button
                v-if="active?.callId !== c.id"
                class="text-[11px] px-2 py-0.5 rounded-md bg-surface-2 hover:bg-surface transition-colors"
                @click="startSupervise(c)"
              >
                Listen
              </button>
              <template v-else>
                <button
                  v-for="m in (['monitor', 'whisper', 'barge'] as const)"
                  :key="m"
                  class="text-[11px] px-2 py-0.5 rounded-md capitalize transition-colors"
                  :class="active.mode === m ? 'bg-accent text-white' : 'bg-surface-2 hover:bg-surface'"
                  @click="setMode(m)"
                >
                  {{ m }}
                </button>
                <button
                  class="text-[11px] px-2 py-0.5 rounded-md bg-danger-soft text-danger transition-colors"
                  @click="stopSupervise()"
                >
                  Stop
                </button>
              </template>
            </div>
          </div>
          </div>
        </Panel>

        <div class="space-y-4">
          <Panel>
            <template #title>Queues by team</template>
            <template #actions>
              <span class="text-xs text-ink-muted">{{ queues.total }} waiting</span>
            </template>
            <div class="max-h-56 overflow-y-auto">
            <div v-if="!queues.queues.length" class="px-5 py-6 text-sm text-ink-faint text-center">
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
              <span class="text-xs text-ink-muted">{{ onlineCount }} online · {{ queues.on_call }} on call</span>
            </template>
            <div class="max-h-80 overflow-y-auto">
            <div v-if="!agents.length" class="px-5 py-6 text-sm text-ink-faint text-center">
              No agents
            </div>
            <div
              v-for="a in sortedAgents"
              :key="a.id"
              class="flex items-center gap-2.5 px-4 py-2 text-[12.5px] border-b border-border last:border-b-0"
            >
              <SignalDot :state="(a.status === 'unknown' ? 'offline' : a.status) ?? 'offline'" hide-label size="sm" />
              <span class="flex-1 truncate">{{ a.display_name || a.sip_username }}</span>
              <span class="text-[11px] text-ink-muted tabular-nums">{{ agentStatusLabel(a) }}</span>
            </div>
            </div>
          </Panel>
        </div>
      </div>
    </template>
  </div>
</template>

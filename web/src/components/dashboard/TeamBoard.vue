<script setup lang="ts">
// TeamBoard — per-team staffing, waits and SLA, worst first. Auto shows cards
// for up to CARDS_MAX_TEAMS teams and a table beyond that; Cards/Rows force one.
import { computed, ref, watch } from "vue";
import { vPulse } from "../../lib/pulse";
import { RouterLink } from "vue-router";
import type { QueueDepth } from "../../api/types";
import {
  belowMinCount,
  fmtMMSS,
  isUnderstaffed,
  longestWait,
  sortTeams,
  staffedPct,
  staffingSegments,
  type StaffSegment,
  teamBadge,
  teamView,
  todayHours,
  waitTone,
  type TeamViewMode,
} from "../../lib/liveDashboard";
import { Panel, SignalDot } from "../ui";

const props = defineProps<{ teams: QueueDepth[]; nowMs: number }>();

// The chosen view survives reloads; storage may be unavailable (private mode).
const VIEW_KEY = "tring.dashboard.teamView";
const MODES: { key: TeamViewMode; label: string }[] = [
  { key: "auto", label: "Auto" },
  { key: "cards", label: "Cards" },
  { key: "rows", label: "Rows" },
];
function loadMode(): TeamViewMode {
  try {
    const v = localStorage.getItem(VIEW_KEY);
    if (v === "auto" || v === "cards" || v === "rows") return v;
  } catch {
    /* storage blocked */
  }
  return "auto";
}
const mode = ref<TeamViewMode>(loadMode());
watch(mode, (v) => {
  try {
    localStorage.setItem(VIEW_KEY, v);
  } catch {
    /* storage blocked */
  }
});

const sorted = computed(() => sortTeams(props.teams, props.nowMs));
const view = computed(() => teamView(mode.value, sorted.value.length));
const below = computed(() => belowMinCount(props.teams));

// A closed team nobody is waiting on shows only its badge and hours.
const compact = (t: QueueDepth) => !t.open && t.waiting_since.length === 0;
const hours = (t: QueueDepth) => todayHours(t.hours, new Date(props.nowMs));
const badge = (t: QueueDepth) => teamBadge(t, props.nowMs);
const longest = (t: QueueDepth) => longestWait(t, props.nowMs);
// Longest wait colour: amber from 80% of the SLA, red past it (theme tokens, like the KPI numbers).
const longestStyle = (t: QueueDepth) => {
  const tone = waitTone(longest(t), t.sla_seconds);
  return tone ? { color: `var(--${tone})` } : undefined;
};
const slaLabel = (t: QueueDepth) => (t.sla_seconds > 0 ? fmtMMSS(t.sla_seconds) : "none");
const SEG_STYLE: Record<StaffSegment["kind"], { bg: string; opacity?: number; title: (n: number) => string }> = {
  on_call: { bg: "var(--color-oncall)", title: (n) => `${n} on call` },
  available: { bg: "var(--color-online)", title: (n) => `${n} available` },
  missing: { bg: "var(--border)", title: (n) => `${n} missing to minimum` },
  away: { bg: "var(--color-away)", opacity: 0.5, title: (n) => `${n} away` },
};
const segStyle = (g: StaffSegment) =>
  `width: ${g.pct}%; background: ${SEG_STYLE[g.kind].bg};` + (SEG_STYLE[g.kind].opacity ? ` opacity: ${SEG_STYLE[g.kind].opacity}` : "");
const settingsLink = (t: QueueDepth) => ({ name: "team", params: { name: t.team } });
</script>

<template>
  <Panel pad>
    <template #title>
      <span class="hstack gap-2">
        Teams
        <span v-if="below > 0" class="badge" data-variant="danger">{{ below }} below minimum</span>
        <small class="text-light">{{ sorted.length }} teams</small>
      </span>
    </template>
    <template #actions>
      <span class="hstack gap-1" role="group" aria-label="Team view">
        <button
          v-for="m in MODES"
          :key="m.key"
          type="button"
          class="small"
          :class="{ outline: mode !== m.key }"
          :aria-pressed="mode === m.key"
          @click="mode = m.key"
        >
          {{ m.label }}
        </button>
      </span>
    </template>

    <div v-if="!sorted.length" class="text-lighter"><small>No teams</small></div>

    <div v-else-if="view === 'cards'" class="row">
      <div v-for="t in sorted" :key="t.team" class="card col-3" :class="{ 'text-lighter': compact(t) }">
        <div class="vstack gap-2">
          <div class="hstack justify-between">
            <span class="hstack gap-1">
              <strong>{{ t.team }}</strong>
              <RouterLink :to="settingsLink(t)" class="button ghost small" :aria-label="`${t.team} settings`" title="Settings"
                >⚙</RouterLink
              >
            </span>
            <span class="badge" :data-variant="badge(t).variant">{{ badge(t).text }}</span>
          </div>
          <small class="text-light"
            >{{ hours(t) }}<template v-if="!compact(t)"> · <span>SLA&nbsp;{{ slaLabel(t) }}</span></template></small
          >

          <template v-if="!compact(t)">
            <small v-if="!t.staffing_known" class="text-lighter">staffing unknown</small>
            <template v-else>
              <div class="hstack justify-between">
                <span class="hstack gap-2">
                  <h3>{{ t.live }}</h3>
                  <small class="text-light">{{ t.min_live_agents > 0 ? `of ${t.min_live_agents} min live` : "live" }}</small>
                </span>
                <span v-if="isUnderstaffed(t)" class="badge" data-variant="danger">{{ staffedPct(t) }}% staffed</span>
              </div>

              <div
                v-if="t.min_live_agents > 0"
                class="flex"
                style="height: 8px; border-radius: 4px; overflow: hidden; background: var(--border)"
                role="img"
                :aria-label="`${t.team}: ${t.live} of ${t.min_live_agents} agents live`"
              >
                <div v-for="g in staffingSegments(t)" :key="g.kind" :style="segStyle(g)" :title="SEG_STYLE[g.kind].title(g.count)"></div>
              </div>

              <small class="hstack gap-2">
                <SignalDot state="on_call" size="sm" :label="`${t.on_call} on call`" />
                <SignalDot state="online" size="sm" :label="`${t.available} available`" />
                <SignalDot state="away" size="sm" :label="`${t.away} away`" />
              </small>
            </template>

            <div class="hstack mt-2"><hr class="w-100" /></div>

            <div class="hstack justify-between gap-2">
              <div class="vstack gap-1">
                <small v-pulse="t.waiting_since.length > 0" :style="t.waiting_since.length ? { color: 'var(--danger)' } : undefined"><strong>{{ t.waiting_since.length }}</strong></small>
                <small class="text-light">waiting</small>
              </div>
              <div class="vstack gap-1" :title="`SLA ${slaLabel(t)}`">
                <small class="mono" :style="longestStyle(t)"><strong>{{ fmtMMSS(longest(t)) }}</strong></small>
                <small class="text-light">longest</small>
              </div>
              <div class="vstack gap-1" title="SLA breaches today">
                <small><strong>{{ t.breaches_today }}</strong></small>
                <small class="text-light">breaches</small>
              </div>
            </div>
          </template>
        </div>
      </div>
    </div>

    <div v-else class="table">
      <table>
        <thead>
          <tr>
            <th>Team</th>
            <th>Status</th>
            <th>Hours</th>
            <th>SLA</th>
            <th>Live / min</th>
            <th>On call</th>
            <th>Available</th>
            <th>Away</th>
            <th>Waiting</th>
            <th>Longest</th>
            <th>Breaches today</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in sorted" :key="t.team" :class="{ 'text-lighter': compact(t) }">
            <td><strong>{{ t.team }}</strong></td>
            <td><span class="badge" :data-variant="badge(t).variant">{{ badge(t).text }}</span></td>
            <td><small>{{ hours(t) }}</small></td>
            <template v-if="!compact(t)">
              <td class="mono"><small>{{ slaLabel(t) }}</small></td>
              <td v-if="!t.staffing_known" colspan="4" class="text-lighter"><small>staffing unknown</small></td>
              <td v-if="t.staffing_known">
                <div class="vstack gap-1">
                  <small class="mono">{{ t.live }}<template v-if="t.min_live_agents > 0"> / {{ t.min_live_agents }}</template></small>
                  <div
                    v-if="t.min_live_agents > 0"
                    class="flex"
                    style="height: 8px; border-radius: 4px; overflow: hidden; background: var(--border)"
                    role="img"
                    :aria-label="`${t.team}: ${t.live} of ${t.min_live_agents} agents live`"
                  >
                    <div v-for="g in staffingSegments(t)" :key="g.kind" :style="segStyle(g)" :title="SEG_STYLE[g.kind].title(g.count)"></div>
                  </div>
                </div>
              </td>
              <template v-if="t.staffing_known">
                <td class="mono"><small>{{ t.on_call }}</small></td>
                <td class="mono"><small>{{ t.available }}</small></td>
                <td class="mono"><small>{{ t.away }}</small></td>
              </template>
              <td class="mono"><small v-pulse="t.waiting_since.length > 0" :style="t.waiting_since.length ? { color: 'var(--danger)' } : undefined">{{ t.waiting_since.length }}</small></td>
              <td><small class="mono" :style="longestStyle(t)">{{ fmtMMSS(longest(t)) }}</small></td>
              <td class="mono"><small>{{ t.breaches_today }}</small></td>
            </template>
            <td v-else colspan="8"></td>
            <td>
              <RouterLink :to="settingsLink(t)" class="button ghost small" :aria-label="`${t.team} settings`" title="Settings"
                >⚙</RouterLink
              >
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </Panel>
</template>

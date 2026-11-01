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
  teamState,
  teamView,
  todayHours,
  waitTone,
  type TeamViewMode,
} from "../../lib/liveDashboard";
import { Panel } from "../ui";

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
// Card edge: red when critical, amber when at risk, muted when closed (mock's .team.crit/.warn/.closed).
const cardClass = (t: QueueDepth) => {
  const st = teamState(t, props.nowMs);
  return st === "ok" ? undefined : st;
};
const settingsLink = (t: QueueDepth) => ({ name: "team", params: { name: t.team } });
</script>

<template>
  <Panel class="team-board">
    <template #title>
      <span class="hstack tb-title">
        Teams
        <span class="tb-sum">
          <template v-if="below > 0"><span class="tb-danger">{{ below }} below minimum</span> · </template>{{ sorted.length }} teams
        </span>
      </span>
    </template>
    <template #actions>
      <span class="tb-seg" role="group" aria-label="Team view">
        <button v-for="m in MODES" :key="m.key" type="button" :aria-pressed="mode === m.key" @click="mode = m.key">
          {{ m.label }}
        </button>
      </span>
    </template>

    <div v-if="!sorted.length" class="tb-empty">No teams</div>

    <div v-else-if="view === 'cards'" class="teams">
      <div v-for="t in sorted" :key="t.team" class="team" :class="cardClass(t)">
        <div class="team-h">
          <span class="name">{{ t.team }}</span>
          <span class="badge" :data-variant="badge(t).variant">{{ badge(t).text }}</span>
          <RouterLink :to="settingsLink(t)" class="gear" :aria-label="`${t.team} settings`" title="Settings">⚙</RouterLink>
        </div>
        <div class="sub">{{ hours(t) }}<template v-if="!compact(t)"> · SLA&nbsp;{{ slaLabel(t) }}</template></div>

        <template v-if="!compact(t)">
          <div v-if="!t.staffing_known" class="sub">staffing unknown</div>
          <template v-else>
            <div class="staff">
              <span class="big" :class="{ 'tb-danger': isUnderstaffed(t) }">{{ t.live }}</span>
              <span class="tb-muted">{{ t.min_live_agents > 0 ? `of ${t.min_live_agents} min live` : "live" }}</span>
              <span v-if="isUnderstaffed(t)" class="pct">{{ staffedPct(t) }}% staffed</span>
            </div>

            <div
              v-if="t.min_live_agents > 0"
              class="sbar"
              role="img"
              :aria-label="`${t.team}: ${t.live} of ${t.min_live_agents} agents live`"
            >
              <div v-for="g in staffingSegments(t)" :key="g.kind" :style="segStyle(g)" :title="SEG_STYLE[g.kind].title(g.count)"></div>
            </div>

            <div class="legend">
              <span style="--c: var(--color-oncall)">{{ t.on_call }} on call</span>
              <span style="--c: var(--color-online)">{{ t.available }} available</span>
              <span style="--c: var(--color-away)">{{ t.away }} away</span>
            </div>
          </template>

          <div class="team-stats">
            <div>
              <div v-pulse="t.waiting_since.length > 0" class="v" :class="{ 'tb-danger': t.waiting_since.length > 0 }">{{ t.waiting_since.length }}</div>
              <div class="k">waiting</div>
            </div>
            <div>
              <div class="v mono" :style="longestStyle(t)">{{ fmtMMSS(longest(t)) }}</div>
              <div class="k">longest · SLA {{ slaLabel(t) }}</div>
            </div>
            <div>
              <div class="v" :class="{ 'tb-danger': t.breaches_today > 0 }">{{ t.breaches_today }}</div>
              <div class="k">SLA breaches today</div>
            </div>
          </div>
        </template>
      </div>
    </div>

    <div v-else class="table">
      <table class="tb-rows">
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
            <td>{{ hours(t) }}</td>
            <template v-if="!compact(t)">
              <td class="mono">{{ slaLabel(t) }}</td>
              <td v-if="!t.staffing_known" colspan="4" class="text-lighter">staffing unknown</td>
              <td v-if="t.staffing_known">
                <div class="vstack gap-1">
                  <span class="mono">{{ t.live }}<template v-if="t.min_live_agents > 0"> / {{ t.min_live_agents }}</template></span>
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
                <td class="mono">{{ t.on_call }}</td>
                <td class="mono">{{ t.available }}</td>
                <td class="mono">{{ t.away }}</td>
              </template>
              <td class="mono"><span v-pulse="t.waiting_since.length > 0" :style="t.waiting_since.length ? { color: 'var(--danger)' } : undefined">{{ t.waiting_since.length }}</span></td>
              <td><span class="mono" :style="longestStyle(t)">{{ fmtMMSS(longest(t)) }}</span></td>
              <td class="mono">{{ t.breaches_today }}</td>
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

<style scoped>
/* Sizes follow the live-dashboard mock; colours are theme tokens. */
.team-board :deep(.panel-head) { height: auto; padding: 12px 16px; }
.team-board :deep(.panel-title) { font-size: 14px; font-weight: 600; line-height: 1.45; }
.tb-title { gap: 8px; }
.tb-sum { font-size: 13px; font-weight: 400; line-height: 1.45; color: var(--muted-foreground); }
.tb-danger { color: var(--danger); }
.tb-muted { color: var(--muted-foreground); }
.tb-seg { display: inline-flex; border: 1px solid var(--border); border-radius: var(--radius-medium); overflow: hidden; }
.tb-seg button {
  margin: 0;
  border: 0;
  border-radius: 0;
  font: inherit;
  font-size: 14px;
  line-height: 1.45;
  padding: 4px 10px;
  color: inherit;
  background: var(--card);
  cursor: pointer;
}
.tb-seg button:hover,
.tb-seg button[aria-pressed="true"] { background: var(--secondary); }
.tb-seg button[aria-pressed="true"] { font-weight: 600; }
.tb-empty { padding: 16px; font-size: 12px; color: var(--faint-foreground); }

.teams { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; padding: 12px; }
@media (max-width: 1100px) { .teams { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 600px) { .teams { grid-template-columns: minmax(0, 1fr); } }
.team {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
  font-size: 14px;
  line-height: 1.45;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 10px;
}
.team.crit { border-color: var(--danger); box-shadow: inset 3px 0 0 var(--danger); }
.team.warn { border-color: var(--warning); box-shadow: inset 3px 0 0 var(--warning); }
.team.closed { opacity: 0.7; background: var(--secondary); }
.team-h { display: flex; align-items: center; gap: 8px; }
.team-h .name { flex: 1; min-width: 0; font-size: 15px; font-weight: 600; }
.team .badge { font-size: 12px; font-weight: 400; line-height: 1.45; padding: 1px 8px; border-radius: 999px; white-space: nowrap; }
.gear { padding: 2px 6px; font-size: 15px; line-height: 1.45; color: var(--muted-foreground); text-decoration: none; }
.sub { margin-top: -6px; font-size: 12px; color: var(--faint-foreground); }
.staff { display: flex; align-items: baseline; gap: 6px; }
.big { font-size: 24px; font-weight: 600; font-variant-numeric: tabular-nums; }
.pct { margin-left: auto; font-size: 12px; color: var(--muted-foreground); }
.sbar { display: flex; height: 14px; border-radius: 4px; overflow: hidden; background: var(--border); }
.legend { display: flex; flex-wrap: wrap; gap: 12px; font-size: 12px; color: var(--muted-foreground); }
.legend span::before {
  content: "";
  display: inline-block;
  width: 8px;
  height: 8px;
  margin-right: 4px;
  border-radius: 2px;
  background: var(--c);
}
.team-stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; padding-top: 10px; border-top: 1px solid var(--border); }
.team-stats .v { font-weight: 600; font-variant-numeric: tabular-nums; }
.team-stats .k { font-size: 12px; color: var(--muted-foreground); }

.tb-rows th { font-size: 12px; font-weight: 500; line-height: 1.45; color: var(--muted-foreground); white-space: nowrap; }
.tb-rows td { font-size: 14px; line-height: 1.45; vertical-align: middle; }
</style>

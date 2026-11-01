<script setup lang="ts">
// TeamBoard — per-team staffing, waits and SLA, worst first. Auto shows cards
// for up to CARDS_MAX_TEAMS teams and a table beyond that; Cards/Rows force one.
import { computed, ref, watch } from "vue";
import { RouterLink } from "vue-router";
import type { QueueDepth } from "../../api/types";
import {
  belowMinCount,
  fmtMMSS,
  hoursSummary,
  isUnderstaffed,
  longestWait,
  seatStates,
  showSeats,
  sortTeams,
  staffedPct,
  teamBadge,
  teamView,
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
const badge = (t: QueueDepth) => teamBadge(t, props.nowMs);
const longest = (t: QueueDepth) => longestWait(t, props.nowMs);
const longestVariant = (t: QueueDepth) => waitTone(longest(t), t.sla_seconds) || "secondary";
const slaLabel = (t: QueueDepth) => (t.sla_seconds > 0 ? fmtMMSS(t.sla_seconds) : "none");
const barMax = (t: QueueDepth) => Math.max(t.min_live_agents, t.live);
const settingsLink = (t: QueueDepth) => ({ name: "team", params: { name: t.team } });
</script>

<template>
  <Panel pad>
    <template #title>
      <span class="hstack gap-2">
        Teams
        <span v-if="below > 0" class="badge" data-variant="danger">{{ below }} below minimum</span>
        <span class="text-light">{{ sorted.length }} teams</span>
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

    <div v-if="!sorted.length" class="text-lighter">No teams</div>

    <div v-else-if="view === 'cards'" class="row">
      <div v-for="t in sorted" :key="t.team" class="card col-4" :class="{ 'text-lighter': compact(t) }">
        <div class="vstack gap-2">
          <div class="hstack justify-between">
            <strong>{{ t.team }}</strong>
            <span class="hstack gap-1">
              <span class="badge" :data-variant="badge(t).variant">{{ badge(t).text }}</span>
              <RouterLink :to="settingsLink(t)" class="button ghost small" :aria-label="`${t.team} settings`" title="Settings"
                >⚙</RouterLink
              >
            </span>
          </div>
          <small class="text-light">{{ hoursSummary(t.hours) }}<template v-if="!compact(t)"> · SLA {{ slaLabel(t) }}</template></small>

          <template v-if="!compact(t)">
            <span v-if="!t.staffing_known" class="text-lighter">staffing unknown</span>
            <template v-else>
              <div class="hstack justify-between">
                <span class="hstack gap-2">
                  <h2>{{ t.live }}</h2>
                  <span class="text-light">{{ t.min_live_agents > 0 ? `of ${t.min_live_agents} min live` : "live" }}</span>
                </span>
                <span v-if="isUnderstaffed(t)" class="badge" data-variant="danger">{{ staffedPct(t) }}% staffed</span>
              </div>

              <span v-if="showSeats(t)" class="hstack gap-1" :aria-label="`${t.team} seats`">
                <SignalDot v-for="(s, i) in seatStates(t)" :key="i" :state="s" hide-label size="sm" />
              </span>
              <progress
                v-else-if="t.min_live_agents > 0"
                :value="t.live"
                :max="barMax(t)"
                :aria-label="`${t.team}: ${t.live} of ${t.min_live_agents} agents live`"
              ></progress>

              <span class="hstack gap-4">
                <SignalDot state="on_call" size="sm" :label="`${t.on_call} on call`" />
                <SignalDot state="online" size="sm" :label="`${t.available} available`" />
                <SignalDot v-if="t.away > 0" state="away" size="sm" :label="`${t.away} away`" />
              </span>
            </template>

            <hr />

            <div class="hstack justify-between">
              <div class="vstack gap-1">
                <strong>{{ t.waiting_since.length }}</strong>
                <small class="text-light">waiting</small>
              </div>
              <div class="vstack gap-1">
                <span><span class="badge mono" :data-variant="longestVariant(t)">{{ fmtMMSS(longest(t)) }}</span></span>
                <small class="text-light">longest · SLA {{ slaLabel(t) }}</small>
              </div>
              <div class="vstack gap-1">
                <strong>{{ t.breaches_today }}</strong>
                <small class="text-light">SLA breaches today</small>
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
            <td><small>{{ hoursSummary(t.hours) }}</small></td>
            <template v-if="!compact(t)">
              <td class="mono">{{ slaLabel(t) }}</td>
              <td v-if="!t.staffing_known" colspan="4" class="text-lighter">staffing unknown</td>
              <td v-if="t.staffing_known">
                <div class="vstack gap-1">
                  <span class="mono">{{ t.live }}<template v-if="t.min_live_agents > 0"> / {{ t.min_live_agents }}</template></span>
                  <progress
                    v-if="t.min_live_agents > 0"
                    :value="t.live"
                    :max="barMax(t)"
                    :aria-label="`${t.team}: ${t.live} of ${t.min_live_agents} agents live`"
                  ></progress>
                </div>
              </td>
              <template v-if="t.staffing_known">
                <td class="mono">{{ t.on_call }}</td>
                <td class="mono">{{ t.available }}</td>
                <td class="mono">{{ t.away }}</td>
              </template>
              <td class="mono">{{ t.waiting_since.length }}</td>
              <td><span class="badge mono" :data-variant="longestVariant(t)">{{ fmtMMSS(longest(t)) }}</span></td>
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

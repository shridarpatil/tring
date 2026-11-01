<script setup lang="ts">
// TeamBoard — per-team staffing, waits and SLA, worst first. Up to
// CARDS_MAX_TEAMS teams render as cards (stacking on phones); more as a table.
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { QueueDepth } from "../../api/types";
import {
  CARDS_MAX_TEAMS,
  fmtMMSS,
  hoursSummary,
  longestWait,
  sortTeams,
  teamBadge,
  waitTone,
} from "../../lib/liveDashboard";

const props = defineProps<{ teams: QueueDepth[]; nowMs: number }>();

const sorted = computed(() => sortTeams(props.teams, props.nowMs));
const asCards = computed(() => sorted.value.length <= CARDS_MAX_TEAMS);

// A closed team nobody is waiting on shows only its badge and hours.
const compact = (t: QueueDepth) => !t.open && t.waiting_since.length === 0;
const badge = (t: QueueDepth) => teamBadge(t, props.nowMs);
const longest = (t: QueueDepth) => longestWait(t, props.nowMs);
const longestVariant = (t: QueueDepth) => waitTone(longest(t), t.sla_seconds) || "secondary";
const slaLabel = (t: QueueDepth) => (t.sla_seconds > 0 ? fmtMMSS(t.sla_seconds) : "none");
const meterMax = (t: QueueDepth) => Math.max(t.min_live_agents, t.live);
const settingsLink = (t: QueueDepth) => ({ name: "team", params: { name: t.team } });
</script>

<template>
  <div v-if="!sorted.length" class="card text-lighter">No teams</div>

  <div v-else-if="asCards" class="row">
    <div v-for="t in sorted" :key="t.team" class="card col-4" :class="{ 'text-lighter': compact(t) }">
      <div class="vstack gap-2">
        <div class="hstack justify-between">
          <strong>{{ t.team }}</strong>
          <span class="badge" :data-variant="badge(t).variant">{{ badge(t).text }}</span>
        </div>
        <small class="text-light">{{ hoursSummary(t.hours) }}<template v-if="!compact(t)"> · SLA {{ slaLabel(t) }}</template></small>

        <template v-if="!compact(t)">
          <div>
            <div class="hstack justify-between">
              <span>Live agents</span>
              <strong class="mono">{{ t.live }}<template v-if="t.min_live_agents > 0"> / {{ t.min_live_agents }}</template></strong>
            </div>
            <meter
              v-if="t.min_live_agents > 0"
              min="0"
              :max="meterMax(t)"
              :value="t.live"
              :low="t.min_live_agents"
              :high="t.min_live_agents"
              :optimum="meterMax(t)"
              :aria-label="`${t.team}: ${t.live} of ${t.min_live_agents} agents live`"
            ></meter>
          </div>
          <small class="text-light">{{ t.on_call }} on call · {{ t.available }} available · {{ t.away }} away</small>
          <div class="hstack justify-between">
            <span>{{ t.waiting_since.length }} waiting</span>
            <span class="hstack gap-1">
              <small class="text-light">longest</small>
              <span class="badge mono" :data-variant="longestVariant(t)">{{ fmtMMSS(longest(t)) }}</span>
            </span>
          </div>
          <div class="hstack justify-between">
            <small class="text-light">{{ t.breaches_today }} SLA breaches today</small>
            <RouterLink :to="settingsLink(t)">Settings</RouterLink>
          </div>
        </template>
      </div>
    </div>
  </div>

  <div v-else class="card">
    <div class="table">
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
              <td>
                <div class="vstack gap-1">
                  <span class="mono">{{ t.live }}<template v-if="t.min_live_agents > 0"> / {{ t.min_live_agents }}</template></span>
                  <meter
                    v-if="t.min_live_agents > 0"
                    min="0"
                    :max="meterMax(t)"
                    :value="t.live"
                    :low="t.min_live_agents"
                    :high="t.min_live_agents"
                    :optimum="meterMax(t)"
                    :aria-label="`${t.team}: ${t.live} of ${t.min_live_agents} agents live`"
                  ></meter>
                </div>
              </td>
              <td class="mono">{{ t.on_call }}</td>
              <td class="mono">{{ t.available }}</td>
              <td class="mono">{{ t.away }}</td>
              <td class="mono">{{ t.waiting_since.length }}</td>
              <td><span class="badge mono" :data-variant="longestVariant(t)">{{ fmtMMSS(longest(t)) }}</span></td>
              <td class="mono">{{ t.breaches_today }}</td>
            </template>
            <td v-else colspan="8"></td>
            <td><RouterLink :to="settingsLink(t)">Settings</RouterLink></td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

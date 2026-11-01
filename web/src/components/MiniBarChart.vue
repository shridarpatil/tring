<script setup lang="ts">
import { computed } from "vue";
import type { StatsHourBucket } from "../api/types";

// Labelled calls-per-hour mini bar chart for the dashboard trend strip. Each bar
// carries its count above and its hour below; the last bar (current hour) is
// highlighted in the live-amber signal colour. Calls that waited past their
// team's SLA show as a red segment at the foot of the bar.
const props = defineProps<{ buckets: StatsHourBucket[] }>();

const max = computed(() => Math.max(1, ...props.buckets.map((b) => b.count)));

// Bar height in px, scaled to the busiest hour. Floor so a non-zero hour is
// always visible; zero stays zero.
function barHeight(count: number): number {
  if (count === 0) return 0;
  return Math.max(4, Math.round((count / max.value) * 110));
}

// The breached share of a bar, in px (never taller than the bar).
function breachHeight(b: StatsHourBucket): number {
  if (!b.breaches) return 0;
  if (!b.count) return 3; // breaches with no counted calls still show a sliver
  const h = barHeight(b.count);
  return Math.min(h, Math.max(2, Math.round((h * b.breaches) / b.count)));
}

// "9AM" / "12PM" — local hour, no minutes, no leading zero.
function hourLabel(iso: string): string {
  return new Date(iso)
    .toLocaleTimeString([], { hour: "numeric", hour12: true })
    .replace(/\s/g, "")
    .toUpperCase();
}
</script>

<template>
  <div>
    <div class="hstack justify-between">
      <small class="text-lighter">Calls / hour · last 12h</small>
      <small class="hstack gap-1"><span style="width: 0.625rem; height: 0.625rem; border-radius: 2px; background: var(--danger)"></span>calls that waited past SLA</small>
    </div>
    <div class="mbc-row mt-2">
      <div v-for="(b, i) in buckets" :key="b.hour" class="mbc-col">
        <span class="mbc-count" :class="{ hot: i === buckets.length - 1 }">{{ b.count }}<template v-if="b.breaches > 0"> <strong style="color: var(--danger)">·{{ b.breaches }}</strong></template></span>
        <div
          class="mbc-bar"
          :class="{ hot: i === buckets.length - 1 }"
          :style="{ height: Math.max(0, barHeight(b.count) - breachHeight(b)) + 'px' }"
        ></div>
        <div
          v-if="breachHeight(b) > 0"
          :style="{ width: '100%', height: breachHeight(b) + 'px', background: 'var(--danger)' }"
        ></div>
        <span class="mbc-hr" :class="{ hot: i === buckets.length - 1 }">{{ hourLabel(b.hour) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mbc-title { font-size: 11px; color: var(--faint-foreground); margin-bottom: 0.375rem; }
.mbc-row { display: flex; align-items: flex-end; gap: 0.375rem; }
.mbc-col { flex: 1; display: flex; flex-direction: column; align-items: center; }
.mbc-count {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  margin-bottom: 0.125rem;
  color: var(--muted-foreground);
}
.mbc-count.hot { color: var(--color-live); font-weight: 600; }
.mbc-bar {
  width: 100%;
  border-radius: var(--radius-small, 4px) var(--radius-small, 4px) 0 0;
  background: color-mix(in srgb, var(--primary) 80%, transparent);
}
.mbc-bar.hot { background: var(--color-live); }
.mbc-hr { font-size: 11px; margin-top: 0.25rem; white-space: nowrap; color: var(--faint-foreground); }
.mbc-hr.hot { color: var(--color-live); font-weight: 600; }
</style>

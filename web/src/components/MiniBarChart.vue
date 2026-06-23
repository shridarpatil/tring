<script setup lang="ts">
import { computed } from "vue";
import type { StatsHourBucket } from "../api/types";

// Labelled calls-per-hour mini bar chart for the dashboard trend strip. Each bar
// carries its count above and its hour below; the last bar (current hour) is
// highlighted in the live-amber signal colour.
const props = defineProps<{ buckets: StatsHourBucket[] }>();

const max = computed(() => Math.max(1, ...props.buckets.map((b) => b.count)));

// Bar height in px, scaled to the busiest hour. Floor so a non-zero hour is
// always visible; zero stays zero.
function barHeight(count: number): number {
  if (count === 0) return 0;
  return Math.max(4, Math.round((count / max.value) * 56));
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
    <div class="text-[11px] text-ink-faint mb-1.5">Calls / hour · last 12h</div>
    <div class="flex items-end gap-1.5">
      <div
        v-for="(b, i) in buckets"
        :key="b.hour"
        class="flex-1 flex flex-col items-center"
      >
        <span
          class="text-[10px] tabular-nums mb-0.5"
          :class="i === buckets.length - 1 ? 'text-live font-semibold' : 'text-ink-muted'"
          >{{ b.count }}</span
        >
        <div
          class="w-full rounded-t"
          :class="i === buckets.length - 1 ? 'bg-live' : 'bg-accent/80'"
          :style="{ height: barHeight(b.count) + 'px' }"
        ></div>
        <span
          class="text-[9.5px] mt-1 whitespace-nowrap"
          :class="i === buckets.length - 1 ? 'text-live font-semibold' : 'text-ink-faint'"
          >{{ hourLabel(b.hour) }}</span
        >
      </div>
    </div>
  </div>
</template>

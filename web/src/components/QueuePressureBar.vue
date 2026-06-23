<script setup lang="ts">
import { computed } from "vue";

// One team's queue row: name, waiting count, and a pressure bar. The bar fills
// relative to the busiest team and turns amber once the wait reaches `threshold`,
// so a backing-up team reads loudly. Faint at zero.
const props = withDefaults(
  defineProps<{ team: string; waiting: number; max: number; threshold?: number }>(),
  { threshold: 3 },
);

const pressured = computed(() => props.waiting >= props.threshold);
const width = computed(() =>
  props.max > 0 ? Math.round((props.waiting / props.max) * 100) : 0,
);
</script>

<template>
  <div class="px-4 py-2.5 border-b border-border last:border-b-0">
    <div class="flex justify-between text-[12.5px] mb-1.5">
      <span class="truncate">{{ team }}</span>
      <span
        class="tabular-nums font-semibold"
        :class="waiting === 0 ? 'text-ink-faint' : pressured ? 'text-live' : 'text-ink'"
        >{{ waiting }}</span
      >
    </div>
    <div class="h-1.5 rounded-full bg-surface-2 overflow-hidden">
      <div
        class="h-full rounded-full transition-all"
        :class="pressured ? 'bg-live' : 'bg-accent'"
        :style="{ width: width + '%' }"
      ></div>
    </div>
  </div>
</template>

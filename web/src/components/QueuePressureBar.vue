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
  <div class="qpb">
    <div class="qpb-head">
      <span class="truncate">{{ team }}</span>
      <span class="qpb-num" :class="waiting === 0 ? 'zero' : pressured ? 'hot' : ''">{{ waiting }}</span>
    </div>
    <div class="qpb-track">
      <div class="qpb-fill" :class="{ hot: pressured }" :style="{ width: width + '%' }"></div>
    </div>
  </div>
</template>

<style scoped>
.qpb { padding: 0.625rem 1rem; border-bottom: 1px solid var(--border); }
.qpb:last-child { border-bottom: none; }
.qpb-head { display: flex; justify-content: space-between; font-size: 12.5px; margin-bottom: 0.375rem; }
.qpb-num { font-variant-numeric: tabular-nums; font-weight: 600; color: var(--foreground); }
.qpb-num.zero { color: var(--faint-foreground); }
.qpb-num.hot { color: var(--color-live); }
.qpb-track { height: 0.375rem; border-radius: 9999px; background: var(--secondary); overflow: hidden; }
.qpb-fill {
  height: 100%;
  border-radius: 9999px;
  background: var(--primary);
  transition: width var(--transition, 200ms);
}
.qpb-fill.hot { background: var(--color-live); }
</style>

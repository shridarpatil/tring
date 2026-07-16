<script setup lang="ts">
// Signature component: animated audio-level bars for an active media leg.
// Static (no animation) when inactive or under prefers-reduced-motion.
withDefaults(defineProps<{ active?: boolean; bars?: number }>(), { bars: 4 });
</script>

<template>
  <span class="meter" aria-hidden="true">
    <span
      v-for="i in bars"
      :key="i"
      class="bar"
      :class="active ? 'on' : ''"
      :style="active ? { animationDelay: i * 0.12 + 's' } : {}"
    ></span>
  </span>
</template>

<style scoped>
.meter { display: inline-flex; align-items: flex-end; gap: 0.125rem; height: 0.875rem; }
.bar {
  width: 0.125rem;
  height: 0.25rem;
  border-radius: 9999px;
  background: var(--faint-foreground);
  transform-origin: bottom;
}
.bar.on {
  height: 100%;
  background: var(--primary);
  animation: tring-level 0.9s ease-in-out infinite;
}
@media (prefers-reduced-motion: reduce) {
  .bar.on { animation: none; }
}
</style>

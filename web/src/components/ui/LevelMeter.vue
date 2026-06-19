<script setup lang="ts">
// Signature component: animated audio-level bars for an active media leg.
// Static (no animation) when inactive or under prefers-reduced-motion.
withDefaults(defineProps<{ active?: boolean; bars?: number }>(), { bars: 4 });
</script>

<template>
  <span class="inline-flex items-end gap-0.5 h-3.5" aria-hidden="true">
    <span
      v-for="i in bars"
      :key="i"
      class="w-0.5 rounded-full"
      :class="active ? 'h-full bg-accent animate-level' : 'h-1 bg-ink-faint'"
      :style="active ? { animationDelay: i * 0.12 + 's' } : {}"
    ></span>
  </span>
</template>

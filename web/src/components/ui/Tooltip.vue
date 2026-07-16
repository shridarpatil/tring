<script setup lang="ts">
import { ref } from "vue";
defineProps<{ content: string }>();
const show = ref(false);
</script>

<template>
  <span
    class="tt-wrap"
    @mouseenter="show = true"
    @mouseleave="show = false"
    @focusin="show = true"
    @focusout="show = false"
  >
    <slot />
    <span v-if="show" role="tooltip" class="tt-bubble">{{ content }}</span>
  </span>
</template>

<style scoped>
.tt-wrap { position: relative; display: inline-flex; }
.tt-bubble {
  position: absolute;
  bottom: 100%;
  left: 50%;
  transform: translateX(-50%);
  margin-bottom: 0.375rem;
  padding: 0.25rem 0.5rem;
  border-radius: var(--radius-medium);
  background: var(--card);
  border: 1px solid var(--border);
  color: var(--foreground);
  font-size: var(--text-8);
  white-space: nowrap;
  z-index: 50;
  pointer-events: none;
  box-shadow: var(--shadow-medium);
}
</style>

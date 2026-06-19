<script setup lang="ts">
import { ref } from "vue";
defineProps<{ content: string }>();
const show = ref(false);
</script>

<template>
  <span
    class="relative inline-flex"
    @mouseenter="show = true"
    @mouseleave="show = false"
    @focusin="show = true"
    @focusout="show = false"
  >
    <slot />
    <span
      v-if="show"
      role="tooltip"
      class="absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 px-2 py-1 rounded-md bg-overlay border border-border text-xs text-ink whitespace-nowrap z-50 pointer-events-none shadow-lg"
    >
      {{ content }}
    </span>
  </span>
</template>

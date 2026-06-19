<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(
  defineProps<{
    tone?: "neutral" | "success" | "live" | "danger" | "warning" | "accent";
  }>(),
  { tone: "neutral" },
);

const map = {
  neutral: "bg-surface-2 text-ink-muted border-border",
  success: "bg-success-soft text-success border-success/30",
  live: "bg-live-soft text-live border-live/30",
  danger: "bg-danger-soft text-danger border-danger/30",
  warning: "bg-warning-soft text-warning border-warning/30",
  accent: "bg-accent-soft text-accent border-accent/30",
};

const cls = computed(() => map[props.tone]);
</script>

<template>
  <span
    class="inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap"
    :class="cls"
  >
    <slot />
  </span>
</template>

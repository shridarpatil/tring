<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(
  defineProps<{
    tone?: "neutral" | "success" | "live" | "danger" | "warning" | "accent";
  }>(),
  { tone: "neutral" },
);

// Distinct class (`tbadge`, not Oat's `.badge`) so Oat's solid-teal-pill badge
// base does not apply — Tring badges are soft-tinted, bordered, rounded-md.
const cls = computed(() => `tbadge tone-${props.tone}`);
</script>

<template>
  <span :class="cls"><slot /></span>
</template>

<style scoped>
.tbadge {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  border: 1px solid transparent;
  border-radius: var(--radius-medium);
  padding: 0.125rem 0.5rem;
  font-size: var(--text-8);
  font-weight: 500;
  line-height: 1.25;
  white-space: nowrap;
}
.tone-neutral {
  background: var(--secondary);
  color: var(--muted-foreground);
  border-color: var(--border);
}
.tone-success {
  background: var(--color-success-soft);
  color: var(--success);
  border-color: color-mix(in srgb, var(--success) 30%, transparent);
}
.tone-live {
  background: var(--color-live-soft);
  color: var(--color-live);
  border-color: color-mix(in srgb, var(--color-live) 30%, transparent);
}
.tone-danger {
  background: var(--color-danger-soft);
  color: var(--danger);
  border-color: color-mix(in srgb, var(--danger) 30%, transparent);
}
.tone-warning {
  background: var(--color-warning-soft);
  color: var(--warning);
  border-color: color-mix(in srgb, var(--warning) 30%, transparent);
}
.tone-accent {
  background: var(--color-accent-soft);
  color: var(--primary);
  border-color: color-mix(in srgb, var(--primary) 30%, transparent);
}
</style>

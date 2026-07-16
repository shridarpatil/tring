<script setup lang="ts">
withDefaults(
  defineProps<{
    title: string;
    variant?: "ghost" | "secondary" | "danger";
    size?: "sm" | "md";
    type?: "button" | "submit";
    disabled?: boolean;
  }>(),
  { variant: "ghost", size: "md", type: "button" },
);
</script>

<template>
  <button
    :type="type"
    :title="title"
    :aria-label="title"
    :disabled="disabled"
    class="icon-btn"
    :class="[`v-${variant}`, `s-${size}`]"
  >
    <slot />
  </button>
</template>

<style scoped>
/* Reset Oat's opinionated bare-<button> base (teal bg, padding). */
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  background: transparent;
  color: var(--muted-foreground);
  border: 1px solid transparent;
  border-radius: var(--radius-medium);
  transition: color var(--transition-fast), background-color var(--transition-fast),
    border-color var(--transition-fast);
}
.icon-btn:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }
.icon-btn:disabled { opacity: 0.5; pointer-events: none; }
.s-sm { height: 1.75rem; width: 1.75rem; }
.s-md { height: 2.25rem; width: 2.25rem; }
.v-ghost:hover:not(:disabled) { color: var(--foreground); background: var(--secondary); }
.v-secondary { background: var(--secondary); color: var(--foreground); border-color: var(--border); }
.v-secondary:hover:not(:disabled) { border-color: var(--color-border-strong); }
.v-danger:hover:not(:disabled) { color: var(--danger); background: var(--color-danger-soft); }
</style>

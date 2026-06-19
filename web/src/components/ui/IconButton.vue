<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(
  defineProps<{
    title: string;
    variant?: "ghost" | "secondary" | "danger";
    size?: "sm" | "md";
    type?: "button" | "submit";
    disabled?: boolean;
  }>(),
  { variant: "ghost", size: "md", type: "button" },
);

const cls = computed(() => {
  const base =
    "inline-flex items-center justify-center rounded-md transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-base disabled:opacity-50 disabled:pointer-events-none";
  const sizes = { sm: "h-7 w-7", md: "h-9 w-9" };
  const variants = {
    ghost: "text-ink-muted hover:text-ink hover:bg-surface-2",
    secondary: "bg-surface-2 text-ink border border-border hover:border-border-strong",
    danger: "text-ink-muted hover:text-danger hover:bg-danger-soft",
  };
  return [base, sizes[props.size], variants[props.variant]];
});
</script>

<template>
  <button :type="type" :title="title" :aria-label="title" :disabled="disabled" :class="cls">
    <slot />
  </button>
</template>

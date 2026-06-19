<script setup lang="ts">
import { computed } from "vue";
import { Loader2 } from "lucide-vue-next";

const props = withDefaults(
  defineProps<{
    variant?: "primary" | "secondary" | "ghost" | "danger";
    size?: "sm" | "md";
    type?: "button" | "submit" | "reset";
    loading?: boolean;
    disabled?: boolean;
    block?: boolean;
  }>(),
  { variant: "primary", size: "md", type: "button" },
);

const cls = computed(() => {
  const base =
    "inline-flex items-center justify-center gap-2 rounded-md font-medium whitespace-nowrap transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-base disabled:opacity-50 disabled:pointer-events-none";
  const sizes = { sm: "h-8 px-3 text-xs", md: "h-9 px-4 text-sm" };
  const variants = {
    primary: "bg-accent text-accent-ink hover:bg-accent-hover",
    secondary: "bg-surface-2 text-ink border border-border hover:border-border-strong",
    ghost: "text-ink-muted hover:text-ink hover:bg-surface-2",
    danger: "bg-danger text-white hover:opacity-90",
  };
  return [base, sizes[props.size], variants[props.variant], props.block ? "w-full" : ""];
});
</script>

<template>
  <button :type="type" :disabled="disabled || loading" :class="cls">
    <Loader2 v-if="loading" class="h-4 w-4 animate-spin" />
    <slot />
  </button>
</template>

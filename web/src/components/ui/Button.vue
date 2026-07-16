<script setup lang="ts">
import { computed } from "vue";

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

const variantAttr = computed(() =>
  props.variant === "secondary" ? "secondary"
  : props.variant === "danger" ? "danger"
  : undefined,
);
const cls = computed(() => [
  props.variant === "ghost" ? "ghost" : "",
  props.size === "sm" ? "small" : "",
  props.block ? "w-100" : "",
]);
</script>

<template>
  <button
    :type="type"
    :data-variant="variantAttr"
    :class="cls"
    :disabled="disabled || loading"
    :aria-busy="loading || undefined"
  >
    <slot />
  </button>
</template>

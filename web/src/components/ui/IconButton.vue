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

const variantAttr = computed(() =>
  props.variant === "secondary" ? "secondary"
  : props.variant === "danger" ? "danger"
  : undefined,
);
const cls = computed(() => [
  "icon",
  props.variant === "ghost" ? "ghost" : "",
  props.size === "sm" ? "small" : "",
]);
</script>

<template>
  <button
    :type="type"
    :title="title"
    :aria-label="title"
    :disabled="disabled"
    :data-variant="variantAttr"
    :class="cls"
  >
    <slot />
  </button>
</template>

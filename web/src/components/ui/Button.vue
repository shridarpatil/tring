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

// primary → bare Oat button; secondary/danger → data-variant; ghost → class.
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
  >
    <Loader2 v-if="loading" class="spin" :size="16" />
    <slot />
  </button>
</template>

<style scoped>
.spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>

<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(
  defineProps<{
    tone?: "neutral" | "success" | "live" | "danger" | "warning" | "accent";
  }>(),
  { tone: "neutral" },
);

// neutral → Oat's 'secondary'; success/warning/danger → Oat variants;
// live/accent → colour-only variants defined in oat-theme.css.
const variantAttr = computed(() => (props.tone === "neutral" ? "secondary" : props.tone));
</script>

<template>
  <span class="badge" :data-variant="variantAttr"><slot /></span>
</template>

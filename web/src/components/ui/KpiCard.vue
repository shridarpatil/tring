<script setup lang="ts">
import Card from "./Card.vue";
defineProps<{ value: string | number; label: string; sub?: string; tone?: "danger" | "warning" | "success" }>();
</script>

<template>
  <!-- Column width (col-3) comes from the parent via class, merged onto Card's root. -->
  <Card class="p-4 vstack gap-1">
    <div class="kpi-num" :style="{ color: tone ? `var(--${tone})` : undefined }">{{ value }}</div>
    <small class="text-light">{{ label }}</small>
    <slot><small v-if="sub" class="text-lighter">{{ sub }}</small></slot>
  </Card>
</template>

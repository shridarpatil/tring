<script setup lang="ts">
import type { Component } from "vue";
defineProps<{ icon?: Component; title: string; hint?: string }>();
</script>

<template>
  <div class="flex flex-col items-center justify-center text-center px-6 py-12">
    <component :is="icon" v-if="icon" class="h-8 w-8 text-ink-faint mb-3" />
    <div class="text-sm font-medium text-ink">{{ title }}</div>
    <p v-if="hint" class="mt-1 text-xs text-ink-faint max-w-xs">{{ hint }}</p>
    <div v-if="$slots.action" class="mt-4"><slot name="action" /></div>
  </div>
</template>

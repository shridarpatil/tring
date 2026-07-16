<script setup lang="ts">
import type { Component } from "vue";
defineProps<{ icon?: Component; title: string; hint?: string }>();
</script>

<template>
  <div class="empty">
    <component :is="icon" v-if="icon" :size="32" class="empty-icon" />
    <div class="empty-title">{{ title }}</div>
    <p v-if="hint" class="empty-hint">{{ hint }}</p>
    <div v-if="$slots.action" class="empty-action"><slot name="action" /></div>
  </div>
</template>

<style scoped>
.empty { display: flex; flex-direction: column; align-items: center; text-align: center; padding: 3rem 1.5rem; }
.empty-icon { color: var(--faint-foreground); margin-bottom: 0.75rem; }
.empty-title { font-size: var(--text-7); font-weight: 500; }
.empty-hint { margin-top: 0.25rem; font-size: var(--text-8); color: var(--faint-foreground); max-width: 20rem; }
.empty-action { margin-top: 1rem; }
</style>

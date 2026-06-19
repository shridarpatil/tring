<script setup lang="ts">
import type { Component } from "vue";
import { useRoute } from "vue-router";

defineProps<{ items: { to: string; label: string; icon: Component }[] }>();
const route = useRoute();

function isActive(to: string): boolean {
  return to === "/" ? route.path === "/" : route.path.startsWith(to);
}
</script>

<template>
  <nav class="flex-1 px-3 py-3 space-y-0.5 overflow-y-auto">
    <RouterLink
      v-for="item in items"
      :key="item.to"
      :to="item.to"
      class="group relative flex items-center gap-2.5 px-3 h-9 rounded-md text-sm transition-colors"
      :class="
        isActive(item.to)
          ? 'bg-surface-2 text-ink font-medium'
          : 'text-ink-muted hover:text-ink hover:bg-surface-2/60'
      "
    >
      <span
        class="absolute left-0 top-1/2 -translate-y-1/2 h-5 w-0.5 rounded-full bg-accent transition-opacity"
        :class="isActive(item.to) ? 'opacity-100' : 'opacity-0'"
      ></span>
      <component
        :is="item.icon"
        class="h-4 w-4 shrink-0"
        :class="isActive(item.to) ? 'text-accent' : ''"
      />
      {{ item.label }}
    </RouterLink>
  </nav>
</template>

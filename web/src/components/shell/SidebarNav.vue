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
  <nav class="nav">
    <RouterLink
      v-for="item in items"
      :key="item.to"
      :to="item.to"
      class="nav-link"
      :class="{ active: isActive(item.to) }"
    >
      <span class="nav-ind"></span>
      <component :is="item.icon" :size="16" class="nav-icon" />
      {{ item.label }}
    </RouterLink>
  </nav>
</template>

<style scoped>
.nav { flex: 1; padding: 0.75rem; display: flex; flex-direction: column; gap: 0.125rem; overflow-y: auto; }
.nav-link {
  position: relative;
  display: flex;
  align-items: center;
  gap: 0.625rem;
  padding: 0 0.75rem;
  height: 2.25rem;
  border-radius: var(--radius-medium);
  font-size: var(--text-7);
  color: var(--muted-foreground);
  text-decoration: none;
  transition: color var(--transition-fast), background-color var(--transition-fast);
}
.nav-link:hover { color: var(--foreground); background: color-mix(in srgb, var(--secondary) 60%, transparent); }
.nav-link.active { background: var(--secondary); color: var(--foreground); font-weight: 500; }
.nav-ind {
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  height: 1.25rem;
  width: 0.125rem;
  border-radius: 9999px;
  background: var(--primary);
  opacity: 0;
  transition: opacity var(--transition-fast);
}
.nav-link.active .nav-ind { opacity: 1; }
.nav-icon { flex-shrink: 0; }
.nav-link.active .nav-icon { color: var(--primary); }
</style>

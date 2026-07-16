<script setup lang="ts">
// BaseNode — vue-flow node card. Node-type header colours are baked in as
// concrete gradients (Tailwind's palette hex, kept identical) since they are
// node-type identity, not theme tokens. Card chrome uses Operator tokens.
import { computed } from "vue";
import { Handle, Position } from "@vue-flow/core";

const props = withDefaults(
  defineProps<{
    label: string;
    headerClass: string;
    hasInput?: boolean;
    outputHandles?: { id: string; label: string; title?: string }[];
  }>(),
  { hasInput: true },
);

// key (kept from the old Tailwind class names) → concrete gradient.
const gradientMap: Record<string, string> = {
  "bg-blue-600": "linear-gradient(to right, #2563eb, #3b82f6)",
  "bg-purple-600": "linear-gradient(to right, #9333ea, #a855f7)",
  "bg-orange-600": "linear-gradient(to right, #ea580c, #f59e0b)",
  "bg-green-600": "linear-gradient(to right, #16a34a, #10b981)",
  "bg-amber-600": "linear-gradient(to right, #d97706, #eab308)",
  "bg-red-600": "linear-gradient(to right, #dc2626, #f43f5e)",
  "bg-cyan-600": "linear-gradient(to right, #0891b2, #06b6d4)",
  "bg-teal-600": "linear-gradient(to right, #0d9488, #14b8a6)",
  "bg-slate-700": "linear-gradient(to right, #334155, #475569)",
};

const headerBg = computed(
  () => gradientMap[props.headerClass] || "linear-gradient(to right, #334155, #475569)",
);

function handleLeft(idx: number, len: number): string {
  return len === 1 ? "50%" : `${((idx + 1) / (len + 1)) * 100}%`;
}
</script>

<template>
  <div class="base-node">
    <Handle
      v-if="hasInput !== false"
      id="input"
      type="target"
      :position="Position.Top"
      class="node-handle node-handle-in"
      style="z-index: 10"
    />

    <div class="node-head" :style="{ background: headerBg }">
      <slot name="icon" />
      <span class="truncate">{{ label }}</span>
    </div>

    <div class="node-body">
      <slot />
    </div>

    <template v-if="outputHandles && outputHandles.length > 0">
      <Handle
        v-for="(handle, idx) in outputHandles"
        :id="handle.id"
        :key="handle.id"
        type="source"
        :position="Position.Bottom"
        :title="handle.title || handle.label"
        class="node-handle"
        :style="{ left: handleLeft(idx, outputHandles.length), zIndex: 10 }"
      />
      <span
        v-for="(handle, idx) in outputHandles"
        :key="'num-' + handle.id"
        class="node-handle-label"
        :style="{ left: handleLeft(idx, outputHandles.length) }"
        :title="handle.title || handle.label"
        >{{ handle.label }}</span
      >
    </template>
    <template v-else-if="!outputHandles">
      <Handle
        id="default"
        type="source"
        :position="Position.Bottom"
        class="node-handle"
        style="z-index: 10"
      />
    </template>
  </div>
</template>

<style scoped>
.base-node {
  position: relative;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius-medium);
  box-shadow: var(--shadow-large);
  min-width: 12rem;
  width: max-content;
  max-width: 24rem;
  overflow: visible;
  transition: border-color var(--transition-fast);
}
.base-node:hover { border-color: var(--color-border-strong); }
.node-head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  border-radius: var(--radius-medium) var(--radius-medium) 0 0;
  color: #fff;
  font-size: var(--text-8);
  font-weight: 600;
  overflow: hidden;
}
.node-body { padding: 0.625rem 0.75rem; font-size: var(--text-8); color: var(--muted-foreground); }
.node-handle {
  width: 0.875rem !important;
  height: 0.875rem !important;
  border-radius: 9999px !important;
  background: var(--primary) !important;
  border: 2px solid var(--card) !important;
  transition: background-color var(--transition-fast) !important;
}
.node-handle:hover { background: var(--color-accent-hover) !important; }
.node-handle-in { background: var(--faint-foreground) !important; }
.node-handle-in:hover { background: var(--muted-foreground) !important; }
.node-handle-label {
  position: absolute;
  bottom: -18px;
  transform: translateX(-50%);
  font-size: 9px;
  font-weight: 700;
  color: var(--faint-foreground);
  pointer-events: none;
  white-space: nowrap;
}
</style>

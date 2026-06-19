<script setup lang="ts">
// A titled container. Header (title + actions slots) over a divider, then body.
// Body is edge-to-edge by default so a DataTable fits flush; set `pad` for forms.
withDefaults(defineProps<{ pad?: boolean }>(), { pad: false });
</script>

<template>
  <div class="rounded-xl bg-surface border border-border overflow-hidden">
    <div
      v-if="$slots.title || $slots.actions"
      class="flex items-center justify-between gap-3 px-5 h-12 border-b border-border"
    >
      <div class="text-sm font-medium text-ink"><slot name="title" /></div>
      <div class="flex items-center gap-2"><slot name="actions" /></div>
    </div>
    <div :class="pad ? 'p-5' : ''"><slot /></div>
  </div>
</template>

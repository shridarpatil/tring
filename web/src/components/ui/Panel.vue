<script setup lang="ts">
// A titled container. Header (title + actions slots) over a divider, then body.
// Body is edge-to-edge by default so a DataTable fits flush; set `pad` for forms.
withDefaults(defineProps<{ pad?: boolean }>(), { pad: false });
</script>

<template>
  <div class="card panel">
    <div v-if="$slots.title || $slots.actions" class="hstack justify-between panel-head">
      <div class="panel-title"><slot name="title" /></div>
      <div class="hstack"><slot name="actions" /></div>
    </div>
    <div :class="pad ? 'panel-body-pad' : ''"><slot /></div>
  </div>
</template>

<style scoped>
/* Structural only — Oat has no titled-panel primitive. Colours via tokens. */
.panel { padding: 0; overflow: hidden; }
.panel-head {
  height: 3rem;
  padding: 0 1.25rem;
  border-bottom: 1px solid var(--border);
}
.panel-title { font-size: var(--text-7); font-weight: 500; }
.panel-body-pad { padding: 1.25rem; }
</style>

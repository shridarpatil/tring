<script setup lang="ts">
const props = defineProps<{ modelValue: boolean; label?: string; disabled?: boolean }>();
const emit = defineEmits<{ (e: "update:modelValue", v: boolean): void }>();
</script>

<template>
  <label class="tgl" :class="disabled ? 'is-off' : ''">
    <button
      type="button"
      role="switch"
      :aria-checked="modelValue"
      :disabled="disabled"
      class="track"
      :class="modelValue ? 'on' : ''"
      @click="emit('update:modelValue', !modelValue)"
    >
      <span class="knob" :class="modelValue ? 'on' : ''"></span>
    </button>
    <span v-if="label" class="lbl">{{ label }}</span>
  </label>
</template>

<style scoped>
.tgl { display: inline-flex; align-items: center; gap: 0.5rem; cursor: pointer; user-select: none; }
.tgl.is-off { opacity: 0.5; pointer-events: none; }
/* Reset Oat's bare-<button> base. */
.track {
  position: relative;
  height: 1.25rem;
  width: 2.25rem;
  padding: 0;
  border-radius: 9999px;
  background: var(--secondary);
  border: 1px solid var(--border);
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}
.track.on { background: var(--primary); border-color: transparent; }
.track:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }
.knob {
  position: absolute;
  top: 0.125rem;
  left: 0.125rem;
  height: 1rem;
  width: 1rem;
  border-radius: 9999px;
  background: var(--faint-foreground);
  transition: transform var(--transition-fast), background-color var(--transition-fast);
}
.knob.on { transform: translateX(1rem); background: var(--background); }
.lbl { font-size: var(--text-7); color: var(--foreground); }
</style>

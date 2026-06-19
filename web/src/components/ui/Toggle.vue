<script setup lang="ts">
const props = defineProps<{ modelValue: boolean; label?: string; disabled?: boolean }>();
const emit = defineEmits<{ (e: "update:modelValue", v: boolean): void }>();
</script>

<template>
  <label
    class="inline-flex items-center gap-2 cursor-pointer select-none"
    :class="disabled ? 'opacity-50 pointer-events-none' : ''"
  >
    <button
      type="button"
      role="switch"
      :aria-checked="modelValue"
      :disabled="disabled"
      class="relative h-5 w-9 rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-base"
      :class="modelValue ? 'bg-accent' : 'bg-surface-2 border border-border'"
      @click="emit('update:modelValue', !modelValue)"
    >
      <span
        class="absolute top-0.5 left-0.5 h-4 w-4 rounded-full transition-transform"
        :class="modelValue ? 'translate-x-4 bg-base' : 'bg-ink-faint'"
      ></span>
    </button>
    <span v-if="label" class="text-sm text-ink">{{ label }}</span>
  </label>
</template>

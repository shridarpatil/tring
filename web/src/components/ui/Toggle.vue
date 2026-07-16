<script setup lang="ts">
defineProps<{ modelValue: boolean; label?: string; disabled?: boolean }>();
const emit = defineEmits<{ (e: "update:modelValue", v: boolean): void }>();
</script>

<template>
  <label>
    <input
      type="checkbox"
      role="switch"
      :checked="modelValue"
      :disabled="disabled"
      @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
    />
    <span v-if="label">{{ label }}</span>
  </label>
</template>

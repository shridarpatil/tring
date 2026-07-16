<script setup lang="ts">
defineProps<{ modelValue: string; tabs: { value: string; label: string }[] }>();
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
</script>

<template>
  <div role="tablist">
    <button
      v-for="t in tabs"
      :key="t.value"
      type="button"
      role="tab"
      :aria-selected="modelValue === t.value"
      @click="emit('update:modelValue', t.value)"
    >
      {{ t.label }}
    </button>
  </div>
</template>

<script setup lang="ts">
defineProps<{ modelValue: string; tabs: { value: string; label: string }[] }>();
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
</script>

<template>
  <div class="flex items-center gap-1 border-b border-border" role="tablist">
    <button
      v-for="t in tabs"
      :key="t.value"
      type="button"
      role="tab"
      :aria-selected="modelValue === t.value"
      class="relative px-3 h-9 text-sm -mb-px border-b-2 transition-colors focus-visible:outline-none focus-visible:text-ink"
      :class="
        modelValue === t.value
          ? 'text-ink border-accent'
          : 'text-ink-muted border-transparent hover:text-ink'
      "
      @click="emit('update:modelValue', t.value)"
    >
      {{ t.label }}
    </button>
  </div>
</template>

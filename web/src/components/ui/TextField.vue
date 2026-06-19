<script setup lang="ts">
import { computed, useId } from "vue";

const props = withDefaults(
  defineProps<{
    modelValue?: string | number;
    label?: string;
    type?: string;
    placeholder?: string;
    mono?: boolean;
    error?: string;
    hint?: string;
    required?: boolean;
    disabled?: boolean;
    autocomplete?: string;
  }>(),
  { type: "text" },
);

const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
const uid = useId();

const inputCls = computed(() => [
  "w-full rounded-md bg-surface-2 border px-3 h-9 text-sm text-ink placeholder:text-ink-faint transition-colors focus:outline-none focus:ring-2 focus:ring-accent/40 focus:border-accent disabled:opacity-50",
  props.error ? "border-danger" : "border-border",
  props.mono ? "font-mono" : "",
]);
</script>

<template>
  <div>
    <label v-if="label" :for="uid" class="block text-xs font-medium text-ink-muted mb-1">
      {{ label }}<span v-if="required" class="text-danger ml-0.5">*</span>
    </label>
    <input
      :id="uid"
      :type="type"
      :value="modelValue"
      :placeholder="placeholder"
      :required="required"
      :disabled="disabled"
      :autocomplete="autocomplete"
      :class="inputCls"
      @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    />
    <p v-if="error" class="mt-1 text-xs text-danger">{{ error }}</p>
    <p v-else-if="hint" class="mt-1 text-xs text-ink-faint">{{ hint }}</p>
  </div>
</template>

<script setup lang="ts">
import { useId } from "vue";

withDefaults(
  defineProps<{
    modelValue?: string;
    label?: string;
    placeholder?: string;
    rows?: number;
    mono?: boolean;
    error?: string;
    hint?: string;
    required?: boolean;
    disabled?: boolean;
  }>(),
  { rows: 4 },
);

const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
const uid = useId();
</script>

<template>
  <div data-field :aria-invalid="error ? 'true' : undefined">
    <label v-if="label || required" :for="uid">
      {{ label }}<span v-if="required" class="req">*</span>
    </label>
    <textarea
      :id="uid"
      :value="modelValue"
      :placeholder="placeholder"
      :rows="rows"
      :required="required"
      :disabled="disabled"
      :aria-invalid="error ? 'true' : undefined"
      :style="mono ? { fontFamily: 'var(--font-mono)' } : undefined"
      @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
    ></textarea>
    <small v-if="hint && !error" data-hint>{{ hint }}</small>
    <small v-if="error" class="error">{{ error }}</small>
  </div>
</template>

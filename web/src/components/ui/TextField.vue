<script setup lang="ts">
import { useId } from "vue";

withDefaults(
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
</script>

<template>
  <div data-field :aria-invalid="error ? 'true' : undefined">
    <label v-if="label || required" :for="uid">
      {{ label }}<span v-if="required" class="req">*</span>
    </label>
    <input
      :id="uid"
      :type="type"
      :value="modelValue"
      :placeholder="placeholder"
      :required="required"
      :disabled="disabled"
      :autocomplete="autocomplete"
      :aria-invalid="error ? 'true' : undefined"
      :style="mono ? { fontFamily: 'var(--font-mono)' } : undefined"
      @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    />
    <small v-if="hint && !error" data-hint>{{ hint }}</small>
    <small v-if="error" class="error">{{ error }}</small>
  </div>
</template>

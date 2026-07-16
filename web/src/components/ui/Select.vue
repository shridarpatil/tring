<script setup lang="ts">
import { useId } from "vue";

type Opt = { value: string | number; label: string };

defineProps<{
  modelValue?: string | number;
  label?: string;
  options: Opt[];
  placeholder?: string;
  error?: string;
  hint?: string;
  required?: boolean;
  disabled?: boolean;
}>();

const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();
const uid = useId();
</script>

<template>
  <div data-field :aria-invalid="error ? 'true' : undefined">
    <label v-if="label || required" :for="uid">
      {{ label }}<span v-if="required" class="req">*</span>
    </label>
    <select
      :id="uid"
      :value="modelValue"
      :required="required"
      :disabled="disabled"
      :aria-invalid="error ? 'true' : undefined"
      @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
    >
      <option v-if="placeholder" value="" disabled>{{ placeholder }}</option>
      <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
    </select>
    <small v-if="hint && !error" data-hint>{{ hint }}</small>
    <small v-if="error" class="error">{{ error }}</small>
  </div>
</template>

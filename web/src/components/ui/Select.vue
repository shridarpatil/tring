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
  <div class="field">
    <label v-if="label" :for="uid" class="field-label">
      {{ label }}<span v-if="required" class="req">*</span>
    </label>
    <select
      :id="uid"
      :value="modelValue"
      :required="required"
      :disabled="disabled"
      :aria-invalid="!!error"
      @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
    >
      <option v-if="placeholder" value="" disabled>{{ placeholder }}</option>
      <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
    </select>
    <p v-if="error" class="msg err">{{ error }}</p>
    <p v-else-if="hint" class="msg hint">{{ hint }}</p>
  </div>
</template>

<style scoped>
.field-label {
  display: block;
  font-size: var(--text-8);
  font-weight: 500;
  color: var(--muted-foreground);
  margin-bottom: 0.25rem;
}
.req { color: var(--danger); margin-left: 0.125rem; }
.field select { margin-block-start: 0; }
.msg { margin-top: 0.25rem; font-size: var(--text-8); }
.err { color: var(--danger); }
.hint { color: var(--faint-foreground); }
</style>

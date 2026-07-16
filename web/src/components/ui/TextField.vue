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
  <div class="field">
    <label v-if="label" :for="uid" class="field-label">
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
      :aria-invalid="!!error"
      :class="{ mono }"
      @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    />
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
.field input { margin-block-start: 0; }
.mono { font-family: var(--font-mono); }
.msg { margin-top: 0.25rem; font-size: var(--text-8); }
.err { color: var(--danger); }
.hint { color: var(--faint-foreground); }
</style>

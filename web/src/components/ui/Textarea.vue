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
  <div class="field">
    <label v-if="label" :for="uid" class="field-label">
      {{ label }}<span v-if="required" class="req">*</span>
    </label>
    <textarea
      :id="uid"
      :value="modelValue"
      :placeholder="placeholder"
      :rows="rows"
      :required="required"
      :disabled="disabled"
      :aria-invalid="!!error"
      :class="{ mono }"
      @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
    ></textarea>
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
.field textarea { margin-block-start: 0; min-height: 0; }
.mono { font-family: var(--font-mono); }
.msg { margin-top: 0.25rem; font-size: var(--text-8); }
.err { color: var(--danger); }
.hint { color: var(--faint-foreground); }
</style>

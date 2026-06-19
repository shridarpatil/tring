<script setup lang="ts">
import { computed, useId } from "vue";
import { ChevronDown } from "lucide-vue-next";

type Opt = { value: string | number; label: string };

const props = defineProps<{
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

const cls = computed(() => [
  "w-full appearance-none rounded-md bg-surface-2 border pl-3 pr-9 h-9 text-sm text-ink transition-colors focus:outline-none focus:ring-2 focus:ring-accent/40 focus:border-accent disabled:opacity-50",
  props.error ? "border-danger" : "border-border",
]);
</script>

<template>
  <div>
    <label v-if="label" :for="uid" class="block text-xs font-medium text-ink-muted mb-1">
      {{ label }}<span v-if="required" class="text-danger ml-0.5">*</span>
    </label>
    <div class="relative">
      <select
        :id="uid"
        :value="modelValue"
        :required="required"
        :disabled="disabled"
        :class="cls"
        @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
      >
        <option v-if="placeholder" value="" disabled>{{ placeholder }}</option>
        <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select>
      <ChevronDown
        class="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-ink-faint"
      />
    </div>
    <p v-if="error" class="mt-1 text-xs text-danger">{{ error }}</p>
    <p v-else-if="hint" class="mt-1 text-xs text-ink-faint">{{ hint }}</p>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { Copy, Check } from "lucide-vue-next";
import { toast } from "../../lib/toast";

const props = defineProps<{ value: string; label?: string }>();
const copied = ref(false);

async function copy() {
  try {
    await navigator.clipboard.writeText(props.value);
    copied.value = true;
    setTimeout(() => (copied.value = false), 1500);
  } catch {
    toast.error("Couldn't copy to clipboard");
  }
}
</script>

<template>
  <button type="button" class="copy-btn" @click="copy">
    <component :is="copied ? Check : Copy" :size="14" :class="copied ? 'ok' : ''" />
    {{ copied ? "Copied" : label || "Copy" }}
  </button>
</template>

<style scoped>
.copy-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  height: 1.75rem;
  padding: 0 0.5rem;
  background: transparent;
  color: var(--muted-foreground);
  border: 1px solid transparent;
  border-radius: var(--radius-medium);
  font-size: var(--text-8);
  transition: color var(--transition-fast), background-color var(--transition-fast);
}
.copy-btn:hover { color: var(--foreground); background: var(--secondary); }
.copy-btn:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }
.copy-btn .ok { color: var(--success); }
</style>

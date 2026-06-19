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
  <button
    type="button"
    class="inline-flex items-center gap-1.5 rounded-md px-2 h-7 text-xs text-ink-muted hover:text-ink hover:bg-surface-2 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
    @click="copy"
  >
    <component :is="copied ? Check : Copy" class="h-3.5 w-3.5" :class="copied ? 'text-success' : ''" />
    {{ copied ? "Copied" : label || "Copy" }}
  </button>
</template>

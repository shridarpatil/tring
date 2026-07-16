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
  <button type="button" class="ghost small" @click="copy">
    <component :is="copied ? Check : Copy" :size="14" />
    {{ copied ? "Copied" : label || "Copy" }}
  </button>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Hash } from "lucide-vue-next";
import BaseNode from "./BaseNode.vue";

defineOptions({ inheritAttrs: false });

const props = defineProps<{ data: Record<string, any> }>();

const summary = computed(() => {
  const c = props.data?.config || {};
  const parts: string[] = [];
  const min = c.min_digits || 1;
  if (c.max_digits) parts.push(min >= c.max_digits ? `${c.max_digits} digits` : `${min}–${c.max_digits} digits`);
  if (c.store_as) parts.push(`→ ${c.store_as}`);
  return parts.join(", ") || "Gather input";
});

const outputHandles = [
  { id: "default", label: "next" },
  { id: "timeout", label: "T/O", title: "Timed Out - no input received" },
  { id: "max_retries", label: "Max", title: "Max Retries - too many invalid attempts" },
];
</script>

<template>
  <BaseNode
    :label="data?.label || 'Gather'"
    header-class="bg-blue-600"
    :output-handles="outputHandles"
  >
    <template #icon><Hash :size="16" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>

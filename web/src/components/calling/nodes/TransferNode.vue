<script setup lang="ts">
import { computed } from "vue";
import { Users } from "lucide-vue-next";
import BaseNode from "./BaseNode.vue";

defineOptions({ inheritAttrs: false });

const props = defineProps<{ data: Record<string, any> }>();

const summary = computed(() => {
  const c = props.data?.config || {};
  if (c.agent_sip) return `→ agent ${c.agent_sip}`;
  return "→ all agents";
});
</script>

<template>
  <BaseNode
    :label="data?.label || 'Transfer'"
    header-class="bg-amber-600"
    :output-handles="[
      { id: 'completed', label: 'completed' },
      { id: 'no_answer', label: 'no answer' },
    ]"
  >
    <template #icon><Users class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Users } from "lucide-vue-next";
import BaseNode from "./BaseNode.vue";

defineOptions({ inheritAttrs: false });

const props = defineProps<{ data: Record<string, any> }>();

const summary = computed(() => {
  const c = props.data?.config || {};
  if (c.team) return `→ team ${c.team}${c.priority ? " ★ priority" : ""}`;
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
      { id: 'queue_timeout', label: 'queue T/O' },
    ]"
  >
    <template #icon><Users :size="16" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>

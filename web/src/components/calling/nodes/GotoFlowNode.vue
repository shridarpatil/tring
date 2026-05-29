<script setup lang="ts">
import { computed } from "vue";
import { ExternalLink } from "lucide-vue-next";
import BaseNode from "./BaseNode.vue";

defineOptions({ inheritAttrs: false });

const props = defineProps<{ data: Record<string, any> }>();

const summary = computed(() => {
  const flowId = (props.data?.config as Record<string, any>)?.flow_id;
  if (!flowId) return "No flow";
  // Without a flows store, just show the id (the properties panel
  // resolves names).
  return String(flowId).slice(0, 12);
});
</script>

<template>
  <BaseNode
    :label="data?.label || 'Goto Flow'"
    header-class="bg-teal-600"
    :output-handles="[]"
  >
    <template #icon><ExternalLink class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Grid3X3 } from "lucide-vue-next";
import BaseNode from "./BaseNode.vue";

defineOptions({ inheritAttrs: false });

const props = defineProps<{ data: Record<string, any> }>();

const options = computed(() => {
  const opts = props.data?.config?.options || {};
  return Object.entries(opts).sort(([a], [b]) => a.localeCompare(b));
});

const outputHandles = computed(() => {
  const handles: { id: string; label: string; title?: string }[] = [];
  for (const [digit] of options.value) {
    handles.push({ id: `digit:${digit}`, label: digit });
  }
  handles.push({ id: "timeout", label: "T/O", title: "Timed Out - no input received" });
  handles.push({
    id: "max_retries",
    label: "Max",
    title: "Max Retries - too many invalid attempts",
  });
  return handles;
});
</script>

<template>
  <BaseNode
    :notifies="!!data?.config?.webhook_id"
    :label="data?.label || 'Menu'"
    header-class="bg-purple-600"
    :output-handles="outputHandles"
  >
    <template #icon><Grid3X3 :size="16" /></template>
    <div v-if="options.length > 0" style="display:flex;flex-direction:column;gap:0.125rem">
      <div v-for="[digit, opt] in options" :key="digit" class="flex gap-1">
        <span class="mono" style="font-weight:700">{{ digit }}:</span>
        <span class="truncate">{{ (opt as any)?.label || "—" }}</span>
      </div>
    </div>
    <p v-else class="text-lighter">No options</p>
  </BaseNode>
</template>

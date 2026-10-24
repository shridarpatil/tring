<script setup lang="ts">
import { computed } from "vue";
import { Volume2 } from "lucide-vue-next";
import BaseNode from "./BaseNode.vue";

defineOptions({ inheritAttrs: false });

const props = defineProps<{ data: Record<string, any> }>();

const summary = computed(() => {
  const c = props.data?.config || {};
  if (c.greeting_text)
    return c.greeting_text.substring(0, 40) + (c.greeting_text.length > 40 ? "..." : "");
  if (c.audio_file) return c.audio_file;
  return "No audio configured";
});

const outputHandles = [{ id: "default", label: "next" }];
</script>

<template>
  <BaseNode
    :notifies="!!data?.config?.webhook_id"
    :label="data?.label || 'Greeting'"
    header-class="bg-green-600"
    :output-handles="outputHandles"
  >
    <template #icon><Volume2 :size="16" /></template>
    <p class="truncate">{{ summary }}</p>
    <p v-if="data?.config?.interruptible" class="node-hint-ok">
      Interruptible
    </p>
  </BaseNode>
</template>

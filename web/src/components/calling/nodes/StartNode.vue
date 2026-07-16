<script setup lang="ts">
// StartNode — the implicit entry point of every flow. Auto-created by
// the editor (never appears in the palette), can't be deleted, has no
// input handle, and exposes a single "default" output. The backend
// executor treats it as a no-op passthrough that immediately walks to
// the connected target via the "default" edge.
import { PlayCircle } from "lucide-vue-next";
import BaseNode from "./BaseNode.vue";

defineOptions({ inheritAttrs: false });

defineProps<{ data: Record<string, any> }>();

const outputHandles = [{ id: "default", label: "Start", title: "Flow entry point" }];
</script>

<template>
  <BaseNode
    label="Start"
    header-class="bg-slate-700"
    :output-handles="outputHandles"
    :has-input="false"
  >
    <template #icon><PlayCircle :size="16" /></template>
    <p class="text-lighter">Flow entry — connect to the first step.</p>
  </BaseNode>
</template>

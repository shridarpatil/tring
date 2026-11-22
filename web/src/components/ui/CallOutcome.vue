<script setup lang="ts">
// CallOutcome — a call's wrap-up outcome (disposition, the call's own tags,
// notes), read-only. Corrections go through PATCH /v1/calls/{id}/wrap-up.
import type { Call } from "../../api/types";
import Badge from "./Badge.vue";

defineProps<{ call: Call }>();
</script>

<template>
  <div>
    <div class="text-lighter" style="margin-top: 1rem; margin-bottom: 0.5rem; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em">
      Outcome
    </div>
    <div class="vstack">
      <div>
        <Badge v-if="call.disposition" tone="accent">{{ call.disposition.name }}</Badge>
        <span v-else class="text-lighter">No disposition</span>
      </div>
      <div v-if="call.tags?.length" class="hstack" style="flex-wrap: wrap; gap: 0.25rem">
        <Badge v-for="t in call.tags" :key="t.id">{{ t.name }}</Badge>
      </div>
      <div v-if="call.notes" style="white-space: pre-wrap">{{ call.notes }}</div>
    </div>
  </div>
</template>

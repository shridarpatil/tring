<script setup lang="ts">
import { useQueues } from "../../composables/useQueues";
import SignalDot from "../ui/SignalDot.vue";
import LevelMeter from "../ui/LevelMeter.vue";

// Global live call-state, visible from every view — the signature feature at
// shell level. Shares ONE poller with the dashboard via useQueues so /v1/queues
// isn't fetched twice when both are mounted.
const q = useQueues();
</script>

<template>
  <div class="flex items-center gap-4 text-xs">
    <span class="flex items-center gap-2" :title="`${q.on_call} live call(s)`">
      <SignalDot :state="q.on_call > 0 ? 'on_call' : 'offline'" hide-label size="sm" />
      <span class="text-ink-muted">On call</span>
      <span class="font-mono tabular-nums" :class="q.on_call > 0 ? 'text-live' : 'text-ink-faint'">
        {{ q.on_call }}
      </span>
      <LevelMeter v-if="q.on_call > 0" active />
    </span>
    <span class="flex items-center gap-2" :title="`${q.total} caller(s) waiting`">
      <span class="text-ink-muted">Queue</span>
      <span class="font-mono tabular-nums" :class="q.total > 0 ? 'text-warning' : 'text-ink-faint'">
        {{ q.total }}
      </span>
    </span>
  </div>
</template>

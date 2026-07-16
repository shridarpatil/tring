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
  <div class="strip">
    <span class="strip-item" :title="`${q.on_call} live call(s)`">
      <SignalDot :state="q.on_call > 0 ? 'on_call' : 'offline'" hide-label size="sm" />
      <span class="lbl">On call</span>
      <span class="num" :class="q.on_call > 0 ? 'live' : ''">{{ q.on_call }}</span>
      <LevelMeter v-if="q.on_call > 0" active />
    </span>
    <span class="strip-item" :title="`${q.total} caller(s) waiting`">
      <span class="lbl">Queue</span>
      <span class="num" :class="q.total > 0 ? 'warn' : ''">{{ q.total }}</span>
    </span>
  </div>
</template>

<style scoped>
.strip { display: flex; align-items: center; gap: 1rem; font-size: var(--text-8); }
.strip-item { display: flex; align-items: center; gap: 0.5rem; }
.lbl { color: var(--muted-foreground); }
.num { font-family: var(--font-mono); font-variant-numeric: tabular-nums; color: var(--faint-foreground); }
.num.live { color: var(--color-live); }
.num.warn { color: var(--warning); }
</style>

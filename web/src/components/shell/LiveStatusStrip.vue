<script setup lang="ts">
import { ref } from "vue";
import { usePoll } from "../../composables/usePoll";
import { api } from "../../api/client";
import type { Queues } from "../../api/types";
import SignalDot from "../ui/SignalDot.vue";
import LevelMeter from "../ui/LevelMeter.vue";

// Global live call-state, visible from every view — the signature feature at
// shell level. Best-effort: keeps the last value if /v1/queues blips.
const q = ref<Queues>({ total: 0, on_call: 0, queues: [] });

async function load() {
  try {
    q.value = await api.get<Queues>("/v1/queues");
  } catch {
    /* keep last value */
  }
}

usePoll(load, 5000);
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

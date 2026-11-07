<script setup lang="ts">
// CallsTable — the call log table shared by the Calls page, the agent page and
// the team page. Rows are display-only; clicking one emits `select` so the
// caller decides what a detail view means.
import { ref, onUnmounted } from "vue";
import { auth } from "../../stores/auth";
import { toast } from "../../lib/toast";
import { callStatusTone } from "../../lib/status";
import type { Call } from "../../api/types";
import Badge from "./Badge.vue";
import Button from "./Button.vue";

const props = withDefaults(
  defineProps<{
    calls: Call[];
    loading?: boolean;
    // Where to fetch a call's recording; defaults to the list item's own URL.
    recordingUrl?: (c: Call) => string | null;
    // The agent page lists only the viewer's own calls, so the column is noise.
    hideAgent?: boolean;
    // Adds the Details button and pointer cursor; rows emit `select` either way.
    selectable?: boolean;
  }>(),
  { loading: false, recordingUrl: (c: Call) => c.recording_url || null, hideAgent: false, selectable: false },
);
const emit = defineEmits<{ select: [call: Call] }>();

// Recordings are served behind auth, so a plain <a href>/<audio src> can't send
// the bearer token. Fetch the WAV as a blob and bind it to an inline <audio>
// player (a blob URL opened in a new tab would just download).
const recUrl = ref<Record<string, string>>({});
let unmounted = false;
async function playRecording(c: Call) {
  const url = props.recordingUrl(c);
  if (!url || recUrl.value[c.id]) return;
  const token = auth.token();
  const res = await fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : {} });
  if (!res.ok) {
    toast.error("recording not available yet");
    return;
  }
  const blobUrl = URL.createObjectURL(await res.blob());
  if (unmounted) {
    URL.revokeObjectURL(blobUrl);
    return;
  }
  recUrl.value = { ...recUrl.value, [c.id]: blobUrl };
}
onUnmounted(() => {
  unmounted = true;
  Object.values(recUrl.value).forEach((u) => URL.revokeObjectURL(u));
});

function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "—";
}
const cols = () => 7 + (props.hideAgent ? 0 : 1) + (props.selectable ? 1 : 0);
</script>

<template>
  <!-- Cap the log to the viewport so the table scrolls inside its own area
       (sticky header) instead of growing the whole page. -->
  <div class="table calls-scroll">
    <table>
      <thead class="calls-sticky">
        <tr>
          <th>From</th>
          <th>To</th>
          <th v-if="!hideAgent">Agent</th>
          <th>Dir</th>
          <th>Status</th>
          <th>Started</th>
          <th class="align-right">Dur</th>
          <th>Recording</th>
          <th v-if="selectable"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="c in calls" :key="c.id" :style="selectable ? 'cursor: pointer' : undefined" @click="emit('select', c)">
          <td class="mono">
            <div class="truncate" style="max-width: 200px" :title="c.client_ref || c.from_number">
              {{ c.client_ref || c.from_number }}
            </div>
          </td>
          <td class="mono">
            <div class="truncate" style="max-width: 200px" :title="c.to_number">{{ c.to_number }}</div>
          </td>
          <td v-if="!hideAgent">{{ c.agent_name || "—" }}</td>
          <td class="text-light">{{ c.direction }}</td>
          <td><Badge :tone="callStatusTone(c.status)">{{ c.status }}</Badge></td>
          <td class="text-light" style="white-space: nowrap">{{ fmt(c.started_at) }}</td>
          <td class="align-right mono">{{ c.duration_seconds != null ? c.duration_seconds + "s" : "—" }}</td>
          <td>
            <audio
              v-if="recUrl[c.id]"
              :src="recUrl[c.id]"
              controls
              autoplay
              style="height: 2rem"
              @click.stop
            ></audio>
            <button
              v-else-if="recordingUrl(c)"
              type="button"
              class="plain"
              style="color: var(--primary)"
              @click.stop="playRecording(c)"
            >
              Play
            </button>
            <span v-else class="text-lighter">—</span>
          </td>
          <td v-if="selectable" class="align-right" style="white-space: nowrap">
            <Button variant="secondary" size="sm" @click.stop="emit('select', c)">Details</Button>
          </td>
        </tr>
        <tr v-if="!loading && calls.length === 0">
          <td :colspan="cols()" class="align-center text-lighter">No calls</td>
        </tr>
        <tr v-if="loading">
          <td :colspan="cols()" class="align-center text-lighter">Loading…</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
/* Structural only — cap the log to the viewport so it scrolls within its own
   area (sticky header) instead of growing the whole page. Colours via tokens. */
.calls-scroll { max-height: calc(100vh - 16rem); overflow: auto; }
.calls-sticky { position: sticky; top: 0; z-index: 10; background: var(--card); }
</style>

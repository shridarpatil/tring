<script setup lang="ts">
import { ref, computed } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { toast } from "../lib/toast";
import { callStatusTone } from "../lib/status";
import { usePagedList } from "../composables/usePagedList";
import type { Call, IvrStep } from "../api/types";
import { Button, Select, Badge, Modal, TextField, Pagination } from "../components/ui";
import { ChevronRight, ChevronDown } from "lucide-vue-next";

const statusFilter = ref("");
const params = computed(() => ({ status: statusFilter.value }));
const {
  items: calls,
  page,
  pageSize,
  total,
  loading,
  load,
  setPage,
} = usePagedList<Call>("/v1/calls", { params });

// Recordings are served behind auth, so a plain <a href>/<audio src> can't send
// the bearer token. Fetch the WAV as a blob and bind it to an inline <audio>
// player (a blob URL opened in a new tab would just download).
const recUrl = ref<Record<string, string>>({});
async function playRecording(c: Call) {
  if (!c.recording_url || recUrl.value[c.id]) return;
  const token = auth.token();
  const res = await fetch(c.recording_url, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
  if (!res.ok) {
    toast.error("recording not available yet");
    return;
  }
  recUrl.value = { ...recUrl.value, [c.id]: URL.createObjectURL(await res.blob()) };
}

// Per-call expand state for the IVR-path detail row.
const expanded = ref<Set<string>>(new Set());
function toggle(id: string) {
  const s = new Set(expanded.value);
  s.has(id) ? s.delete(id) : s.add(id);
  expanded.value = s;
}
function hasPath(c: Call): boolean {
  return Array.isArray(c.ivr_path) && c.ivr_path.length > 0;
}

// stepText turns one recorded step into a human-readable line, e.g.
// "Menu — pressed 1", "Transfer → alice (no answer)".
function stepText(s: IvrStep): string {
  switch (s.node_type) {
    case "menu":
      if (s.outcome.startsWith("digit:")) return `Menu — pressed ${s.outcome.slice(6)}`;
      if (s.outcome === "timeout") return "Menu — no input (timeout)";
      if (s.outcome === "max_retries")
        return `Menu — invalid input${s.dtmf_input ? ` (${s.dtmf_input})` : ""}`;
      return "Menu";
    case "transfer":
      return `Transfer → ${s.detail || "agent"} (${s.outcome === "completed" ? "answered" : "no answer"})`;
    case "greeting":
      return s.action === "speak" ? `Greeting — “${s.detail}”` : `Greeting — ${s.detail || "audio"}`;
    case "hangup":
      return "Hangup";
    case "start":
      return "Start";
    default:
      return s.outcome ? `${s.node_type} — ${s.outcome}` : s.node_type;
  }
}

const showConnect = ref(false);
const placing = ref(false);
const connectForm = ref({ agent_number: "", client_number: "" });

const statusOptions = [
  { value: "", label: "All" },
  { value: "ringing", label: "Ringing" },
  { value: "answered", label: "Answered" },
  { value: "ended", label: "Ended" },
];

async function connect() {
  placing.value = true;
  try {
    const c = await api.post<Call>("/v1/calls/connect", connectForm.value);
    toast.success(`Call placed — ${c.from_number} → ${c.to_number} (${c.status})`);
    connectForm.value = { agent_number: "", client_number: "" };
    showConnect.value = false;
    await load();
  } catch (e) {
    toast.error(errMessage(e, "connect failed"));
  } finally {
    placing.value = false;
  }
}

function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "—";
}
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold tracking-tight">Calls</h1>
      <Button @click="showConnect = true">Connect two numbers</Button>
    </div>

    <div class="flex items-center gap-2">
      <span class="text-sm text-ink-muted">Status</span>
      <Select v-model="statusFilter" :options="statusOptions" />
    </div>

    <div class="rounded-xl bg-surface border border-border">
      <!-- Cap the log to the viewport so the table scrolls inside its own area
           (sticky header) instead of growing the whole page; the filter above
           and pagination below stay put. -->
      <div class="overflow-auto max-h-[calc(100vh-16rem)]">
        <table class="w-full text-sm">
        <thead class="sticky top-0 z-10 bg-surface">
          <tr class="border-b border-border text-left">
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">From</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">To</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">Agent</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">Dir</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">Status</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">Started</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint text-right">Dur</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">Recording</th>
            <th class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint">IVR path</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="c in calls" :key="c.id">
            <tr
              class="border-b border-border/60 transition-colors"
              :class="hasPath(c) ? 'cursor-pointer hover:bg-surface-2/40' : ''"
              @click="hasPath(c) && toggle(c.id)"
            >
              <td class="px-4 py-2.5 font-mono text-xs text-ink">
                <div class="max-w-[200px] truncate" :title="c.client_ref || c.from_number">
                  {{ c.client_ref || c.from_number }}
                </div>
              </td>
              <td class="px-4 py-2.5 font-mono text-xs text-ink">
                <div class="max-w-[200px] truncate" :title="c.to_number">{{ c.to_number }}</div>
              </td>
              <td class="px-4 py-2.5 text-ink">{{ c.agent_name || "—" }}</td>
              <td class="px-4 py-2.5 text-ink-muted">{{ c.direction }}</td>
              <td class="px-4 py-2.5"><Badge :tone="callStatusTone(c.status)">{{ c.status }}</Badge></td>
              <td class="px-4 py-2.5 text-ink-muted whitespace-nowrap">{{ fmt(c.started_at) }}</td>
              <td class="px-4 py-2.5 text-right font-mono text-xs tabular-nums text-ink">
                {{ c.duration_seconds != null ? c.duration_seconds + "s" : "—" }}
              </td>
              <td class="px-4 py-2.5">
                <audio
                  v-if="recUrl[c.id]"
                  :src="recUrl[c.id]"
                  controls
                  autoplay
                  class="h-8"
                  @click.stop
                ></audio>
                <button
                  v-else-if="c.recording_url"
                  class="text-accent hover:text-accent-hover"
                  @click.stop="playRecording(c)"
                >
                  Play
                </button>
                <span v-else class="text-ink-faint">—</span>
              </td>
              <td class="px-4 py-2.5">
                <button
                  v-if="hasPath(c)"
                  class="inline-flex items-center gap-1 text-ink-muted hover:text-ink"
                  @click.stop="toggle(c.id)"
                >
                  <component :is="expanded.has(c.id) ? ChevronDown : ChevronRight" class="h-3.5 w-3.5" />
                  {{ expanded.has(c.id) ? "Hide" : `View (${c.ivr_path!.length})` }}
                </button>
                <span v-else class="text-ink-faint">—</span>
              </td>
            </tr>
            <tr v-if="hasPath(c) && expanded.has(c.id)" class="bg-surface-2/40 border-b border-border">
              <td colspan="9" class="px-8 py-3">
                <ol class="space-y-1">
                  <li
                    v-for="(step, i) in c.ivr_path"
                    :key="i"
                    class="flex items-center gap-3 text-sm"
                  >
                    <span class="text-ink-faint font-mono tabular-nums w-5 text-right">{{ i + 1 }}.</span>
                    <span class="text-ink">{{ stepText(step) }}</span>
                  </li>
                </ol>
              </td>
            </tr>
          </template>
          <tr v-if="!loading && calls.length === 0">
            <td colspan="9" class="px-4 py-10 text-center text-ink-faint">No calls</td>
          </tr>
          <tr v-if="loading">
            <td colspan="9" class="px-4 py-10 text-center text-ink-faint">Loading…</td>
          </tr>
        </tbody>
      </table>
      </div>
      <Pagination
        :page="page"
        :page-size="pageSize"
        :total="total"
        label="calls"
        @update:page="setPage"
      />
    </div>

    <Modal v-model:open="showConnect" title="Connect two numbers">
      <form id="connect-form" class="space-y-4" @submit.prevent="connect">
        <TextField
          v-model="connectForm.agent_number"
          label="Agent number"
          mono
          required
          placeholder="+919876543210"
        />
        <TextField
          v-model="connectForm.client_number"
          label="Client number"
          mono
          required
          placeholder="+919812345678"
        />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showConnect = false">Cancel</Button>
        <Button type="submit" form="connect-form" :loading="placing">Place call</Button>
      </template>
    </Modal>
  </div>
</template>

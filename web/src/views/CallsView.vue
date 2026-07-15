<script setup lang="ts">
import { ref, computed } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { toast } from "../lib/toast";
import { callStatusTone } from "../lib/status";
import { usePagedList } from "../composables/usePagedList";
import type { Call, CallEvent } from "../api/types";
import { Button, Select, Badge, Modal, TextField, Pagination, CallDetailModal, CallFullLogModal } from "../components/ui";
import { toCallDetail, toFullLog } from "../lib/callDetail";

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

// Detail popup: clicking a row opens the summary modal; "View full log" swaps to
// the wide event table. Both read the same fetched timeline + the call's ivr_path.
const events = ref<Record<string, CallEvent[]>>({});
const loadingEvents = ref<Set<string>>(new Set());
async function loadEvents(id: string) {
  loadingEvents.value = new Set(loadingEvents.value).add(id);
  try {
    events.value = { ...events.value, [id]: await api.get<CallEvent[]>(`/v1/calls/${id}/events`) };
  } catch {
    events.value = { ...events.value, [id]: [] }; // don't retry-loop; show "no history"
  } finally {
    const s = new Set(loadingEvents.value);
    s.delete(id);
    loadingEvents.value = s;
  }
}

const detailCall = ref<Call | null>(null);
const detailOpen = ref(false);
const fullLogOpen = ref(false);

function openDetail(c: Call) {
  detailCall.value = c;
  if (events.value[c.id] === undefined) loadEvents(c.id);
  detailOpen.value = true;
}

const detailProp = computed(() =>
  detailCall.value && events.value[detailCall.value.id] !== undefined
    ? toCallDetail(detailCall.value, events.value[detailCall.value.id])
    : null,
);
const fullLogRows = computed(() =>
  detailCall.value && events.value[detailCall.value.id] !== undefined
    ? toFullLog(detailCall.value, events.value[detailCall.value.id])
    : [],
);

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
          </tr>
        </thead>
        <tbody>
          <template v-for="c in calls" :key="c.id">
            <tr
              class="border-b border-border/60 transition-colors cursor-pointer hover:bg-surface-2/40"
              @click="openDetail(c)"
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
                  class="inline-flex items-center gap-1 text-ink-muted hover:text-ink"
                  @click.stop="openDetail(c)"
                >
                  Details
                </button>
              </td>
            </tr>
          </template>
          <tr v-if="!loading && calls.length === 0">
            <td colspan="8" class="px-4 py-10 text-center text-ink-faint">No calls</td>
          </tr>
          <tr v-if="loading">
            <td colspan="8" class="px-4 py-10 text-center text-ink-faint">Loading…</td>
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

    <CallDetailModal
      :open="detailOpen"
      :call="detailProp"
      @update:open="detailOpen = $event"
      @view-full-log="detailOpen = false; fullLogOpen = true"
    />
    <CallFullLogModal
      :open="fullLogOpen"
      :rows="fullLogRows"
      :caller="detailCall?.client_ref || detailCall?.from_number || ''"
      :callee="detailCall?.to_number || ''"
      @update:open="fullLogOpen = $event"
      @back="fullLogOpen = false; detailOpen = true"
    />
  </div>
</template>

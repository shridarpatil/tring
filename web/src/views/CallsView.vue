<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
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
// Search box: a call id (or its first characters) or a phone number in any
// format. Debounced so typing doesn't fire a request per keystroke.
const search = ref("");
const searchQuery = ref("");
let searchTimer: ReturnType<typeof setTimeout> | undefined;
watch(search, (v) => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => (searchQuery.value = v.trim()), 300);
});
const params = computed(() => ({ status: statusFilter.value, q: searchQuery.value }));
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

// The URL drives the popups so every call has a shareable link:
// /calls/<id> = summary, /calls/<id>?view=log = full log, /calls = list only.
const route = useRoute();
const router = useRouter();

function openDetail(c: Call) {
  detailCall.value = c; // already loaded: the watcher won't refetch it
  router.push({ name: "calls", params: { id: c.id } });
}
function closeCall() {
  router.replace({ name: "calls" });
}
function showFullLog() {
  router.replace({ name: "calls", params: { id: detailCall.value?.id }, query: { view: "log" } });
}
function showSummary() {
  router.replace({ name: "calls", params: { id: detailCall.value?.id } });
}

watch(
  () => [route.params.id, route.query.view] as const,
  async ([rawId, view]) => {
    const id = typeof rawId === "string" ? rawId : "";
    if (!id) {
      detailOpen.value = false;
      fullLogOpen.value = false;
      return;
    }
    if (detailCall.value?.id !== id) {
      // Opened from a link: the call may not be on the current list page.
      const listed = calls.value.find((c) => c.id === id);
      try {
        detailCall.value = listed ?? (await api.get<Call>(`/v1/calls/${id}`));
      } catch (e) {
        toast.error(errMessage(e, "call not found"));
        router.replace({ name: "calls" });
        return;
      }
    }
    if (events.value[id] === undefined) loadEvents(id);
    fullLogOpen.value = view === "log";
    detailOpen.value = view !== "log";
  },
  { immediate: true },
);

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
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">Calls</h1>
      <Button @click="showConnect = true">Connect two numbers</Button>
    </div>

    <div class="hstack">
      <TextField
        v-model="search"
        type="search"
        placeholder="Search call ID or phone number"
        aria-label="Search calls by call ID or phone number"
        style="min-width: 18rem"
      />
      <span class="text-light">Status</span>
      <Select v-model="statusFilter" :options="statusOptions" />
    </div>

    <div class="card" style="padding: 0">
      <!-- Cap the log to the viewport so the table scrolls inside its own area
           (sticky header) instead of growing the whole page; the filter above
           and pagination below stay put. -->
      <div class="table calls-scroll">
        <table>
        <thead class="calls-sticky">
          <tr>
            <th>From</th>
            <th>To</th>
            <th>Agent</th>
            <th>Dir</th>
            <th>Status</th>
            <th>Started</th>
            <th class="align-right">Dur</th>
            <th>Recording</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="c in calls" :key="c.id">
            <tr style="cursor: pointer" @click="openDetail(c)">
              <td class="mono">
                <div class="truncate" style="max-width: 200px" :title="c.client_ref || c.from_number">
                  {{ c.client_ref || c.from_number }}
                </div>
              </td>
              <td class="mono">
                <div class="truncate" style="max-width: 200px" :title="c.to_number">{{ c.to_number }}</div>
              </td>
              <td>{{ c.agent_name || "—" }}</td>
              <td class="text-light">{{ c.direction }}</td>
              <td><Badge :tone="callStatusTone(c.status)">{{ c.status }}</Badge></td>
              <td class="text-light" style="white-space: nowrap">{{ fmt(c.started_at) }}</td>
              <td class="align-right mono">
                {{ c.duration_seconds != null ? c.duration_seconds + "s" : "—" }}
              </td>
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
                  v-else-if="c.recording_url"
                  type="button"
                  class="plain"
                  style="color: var(--primary)"
                  @click.stop="playRecording(c)"
                >
                  Play
                </button>
                <span v-else class="text-lighter">—</span>
              </td>
              <td class="align-right" style="white-space: nowrap">
                <Button variant="secondary" size="sm" @click.stop="openDetail(c)">Details</Button>
              </td>
            </tr>
          </template>
          <tr v-if="!loading && calls.length === 0">
            <td colspan="9" class="align-center text-lighter">No calls</td>
          </tr>
          <tr v-if="loading">
            <td colspan="9" class="align-center text-lighter">Loading…</td>
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
      <form id="connect-form" class="vstack" @submit.prevent="connect">
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
      @update:open="(v) => { if (!v) closeCall(); }"
      @view-full-log="showFullLog"
    />
    <CallFullLogModal
      :open="fullLogOpen"
      :rows="fullLogRows"
      :caller="detailCall?.client_ref || detailCall?.from_number || ''"
      :callee="detailCall?.to_number || ''"
      @update:open="(v) => { if (!v) closeCall(); }"
      @back="showSummary"
    />
  </div>
</template>

<style scoped>
/* Structural only — cap the log to the viewport so it scrolls within its own
   area (sticky header) instead of growing the whole page. Colours via tokens. */
.calls-scroll { max-height: calc(100vh - 16rem); overflow: auto; }
.calls-sticky { position: sticky; top: 0; z-index: 10; background: var(--card); }
</style>

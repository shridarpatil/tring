<script setup lang="ts">
import { ref, computed } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { toast } from "../lib/toast";
import { callStatusTone } from "../lib/status";
import { usePagedList } from "../composables/usePagedList";
import type { Call, IvrStep, CallEvent } from "../api/types";
import { Button, Select, Badge, Modal, TextField, Pagination } from "../components/ui";
import type { Component } from "vue";
import {
  ChevronRight,
  ChevronDown,
  PhoneIncoming,
  BellRing,
  PhoneMissed,
  PhoneCall,
  ArrowRightLeft,
  Headphones,
  PhoneOff,
  Volume2,
  MessageSquare,
  ListTree,
  Flag,
  CircleDot,
} from "lucide-vue-next";

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

// Per-call expand state for the history detail row. Expanding fetches the call's
// durable event timeline (call_events); the IVR path ships with the call row.
const expanded = ref<Set<string>>(new Set());
const events = ref<Record<string, CallEvent[]>>({});
const loadingEvents = ref<Set<string>>(new Set());
function toggle(id: string) {
  const s = new Set(expanded.value);
  s.has(id) ? s.delete(id) : s.add(id);
  expanded.value = s;
  if (s.has(id) && events.value[id] === undefined) loadEvents(id);
}
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
function hasPath(c: Call): boolean {
  return Array.isArray(c.ivr_path) && c.ivr_path.length > 0;
}

// eventText renders one timeline row to a human line: "Ringing Sales",
// "No answer from Alice", "Answered by Bob", "Transfer → Sales (no answer)".
function eventText(e: CallEvent): string {
  const d = e.detail || {};
  const s = (k: string) => (typeof d[k] === "string" ? (d[k] as string) : "");
  switch (e.type) {
    case "started": {
      const from = s("from"), to = s("to");
      return from || to ? `Call started — ${from || "?"} → ${to || "?"}` : "Call started";
    }
    case "ring":
      // Per-agent (agent name); fall back to target for rows from the old format.
      return `Ringing ${s("agent") || s("target") || "agents"}`;
    case "no_answer": {
      const agents = Array.isArray(d.agents)
        ? (d.agents as unknown[]).filter((x): x is string => typeof x === "string")
        : [];
      if (agents.length) return `No answer from ${agents.join(", ")}`;
      return s("target") ? `No answer from ${s("target")}` : "No answer";
    }
    case "answered":
      return s("agent") ? `Answered by ${s("agent")}` : "Answered";
    case "transfer": {
      const to = s("to"), by = s("from");
      const res = s("result") === "completed" ? "answered" : "no answer";
      return `Transfer${to ? ` → ${to}` : ""} (${res})${by ? ` — by ${by}` : ""}`;
    }
    case "supervise":
      return `Supervisor joined${s("mode") ? ` (${s("mode")})` : ""}`;
    case "ended": {
      const cause = s("cause");
      return cause ? `Call ended — ${cause}` : "Call ended";
    }
    default:
      return e.type;
  }
}
function eventTime(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "" : d.toLocaleTimeString();
}

// stepText turns one recorded step into a human-readable line, e.g.
// "Menu — pressed 1", "Transfer → alice (no answer)". Audio prompts show their
// spoken text when known, never the internal .ogg file path (meaningless to an
// operator).
function stepText(s: IvrStep): string {
  switch (s.node_type) {
    case "menu":
      if (s.outcome.startsWith("digit:")) return `Menu — pressed ${s.outcome.slice(6)}`;
      if (s.outcome === "timeout") return "Menu — no input (timeout)";
      if (s.outcome === "max_retries")
        return `Menu — invalid input${s.dtmf_input ? ` (${s.dtmf_input})` : ""}`;
      return "Menu";
    case "gather":
      return s.dtmf_input ? `Gather — entered ${s.dtmf_input}` : "Gather";
    case "transfer":
      return `Transfer → ${s.detail || "agent"} (${s.outcome === "completed" ? "answered" : "no answer"})`;
    case "greeting":
      return s.action === "speak" && s.detail ? `Greeting — “${s.detail}”` : "Played greeting";
    case "hangup":
      return "Hung up";
    case "start":
      return "Call entered flow";
    default:
      return s.outcome ? `${s.node_type} — ${s.outcome}` : s.node_type;
  }
}

// stepIcon maps an IVR node to its glyph — a compact visual key for the flow walk.
function stepIcon(s: IvrStep): Component {
  switch (s.node_type) {
    case "start":
      return Flag;
    case "greeting":
      return s.action === "speak" ? MessageSquare : Volume2;
    case "menu":
    case "gather":
      return ListTree;
    case "transfer":
      return ArrowRightLeft;
    case "hangup":
      return PhoneOff;
    default:
      return CircleDot;
  }
}

// eventMeta styles one timeline event by type: an icon plus a soft/ink tone pair
// so the lifecycle reads at a glance (ringing = amber, no-answer = red, answered
// = green, ended = muted).
function eventMeta(type: string): { icon: Component; ring: string; ink: string } {
  switch (type) {
    case "started":
      return { icon: PhoneIncoming, ring: "bg-accent-soft", ink: "text-accent" };
    case "ring":
      return { icon: BellRing, ring: "bg-warning-soft", ink: "text-warning" };
    case "no_answer":
      return { icon: PhoneMissed, ring: "bg-danger-soft", ink: "text-danger" };
    case "answered":
      return { icon: PhoneCall, ring: "bg-success-soft", ink: "text-success" };
    case "transfer":
      return { icon: ArrowRightLeft, ring: "bg-accent-soft", ink: "text-accent" };
    case "supervise":
      return { icon: Headphones, ring: "bg-accent-soft", ink: "text-accent" };
    case "ended":
      return { icon: PhoneOff, ring: "bg-surface-2", ink: "text-ink-muted" };
    default:
      return { icon: CircleDot, ring: "bg-surface-2", ink: "text-ink-muted" };
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
              class="border-b border-border/60 transition-colors cursor-pointer hover:bg-surface-2/40"
              @click="toggle(c.id)"
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
                  @click.stop="toggle(c.id)"
                >
                  <component :is="expanded.has(c.id) ? ChevronDown : ChevronRight" class="h-3.5 w-3.5" />
                  {{ expanded.has(c.id) ? "Hide" : "Details" }}
                </button>
              </td>
            </tr>
            <tr v-if="expanded.has(c.id)" class="bg-surface-2/30 border-b border-border">
              <td colspan="9" class="px-6 py-5">
                <div class="grid gap-x-12 gap-y-6 md:grid-cols-2 max-w-5xl">
                  <!-- Durable call timeline (call_events): the lifecycle, in time. -->
                  <section>
                    <h4 class="mb-3 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-widest text-ink-faint">
                      Timeline
                      <span v-if="events[c.id]?.length" class="rounded-full bg-surface-2 px-1.5 text-[10px] font-medium text-ink-muted">
                        {{ events[c.id].length }}
                      </span>
                    </h4>
                    <div v-if="loadingEvents.has(c.id)" class="text-sm text-ink-faint">Loading…</div>
                    <ol v-else-if="events[c.id]?.length" class="relative">
                      <li
                        v-for="(e, i) in events[c.id]"
                        :key="i"
                        class="relative flex gap-3 pb-4 last:pb-0"
                      >
                        <!-- connector rail: reaches down to the next node -->
                        <span
                          v-if="i < events[c.id].length - 1"
                          class="absolute left-[13px] top-7 bottom-0 w-px bg-border"
                          aria-hidden="true"
                        ></span>
                        <span
                          class="relative z-10 flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-full ring-1 ring-border/60"
                          :class="eventMeta(e.type).ring"
                        >
                          <component :is="eventMeta(e.type).icon" class="h-3.5 w-3.5" :class="eventMeta(e.type).ink" />
                        </span>
                        <div class="min-w-0 flex-1 pt-0.5">
                          <div class="flex items-baseline justify-between gap-3">
                            <span class="text-sm text-ink">{{ eventText(e) }}</span>
                            <span class="shrink-0 font-mono text-[11px] tabular-nums text-ink-faint">{{ eventTime(e.at) }}</span>
                          </div>
                        </div>
                      </li>
                    </ol>
                    <div v-else class="text-sm text-ink-faint">No history recorded for this call.</div>
                  </section>

                  <!-- IVR navigation (calls.ivr_path): the ordered flow walk. -->
                  <section v-if="hasPath(c)">
                    <h4 class="mb-3 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-widest text-ink-faint">
                      IVR navigation
                      <span class="rounded-full bg-surface-2 px-1.5 text-[10px] font-medium text-ink-muted">
                        {{ c.ivr_path!.length }}
                      </span>
                    </h4>
                    <ol class="space-y-2.5">
                      <li
                        v-for="(step, i) in c.ivr_path"
                        :key="i"
                        class="flex items-center gap-3 text-sm"
                      >
                        <span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-surface-2 font-mono text-[11px] tabular-nums text-ink-muted">
                          {{ i + 1 }}
                        </span>
                        <component :is="stepIcon(step)" class="h-3.5 w-3.5 shrink-0 text-ink-faint" />
                        <span class="text-ink">{{ stepText(step) }}</span>
                      </li>
                    </ol>
                  </section>
                </div>
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

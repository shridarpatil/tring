<script setup lang="ts">
import { computed } from "vue";
import Modal from "./Modal.vue";
import Badge from "./Badge.vue";
import {
  Flag,
  ArrowRightLeft,
  PhoneCall,
  PhoneOff,
  PhoneMissed,
  Headphones,
  PhoneIncoming,
  PhoneOutgoing,
  ListTree,
  X,
} from "lucide-vue-next";
import type { CallDetail, EventIcon, EventKind } from "../../lib/callDetail";

const props = defineProps<{ open: boolean; call: CallDetail | null }>();
const emit = defineEmits<{
  (e: "update:open", v: boolean): void;
  (e: "view-full-log", id: string): void;
}>();

const iconMap: Record<EventIcon, unknown> = {
  flag: Flag,
  transfer: ArrowRightLeft,
  "phone-check": PhoneCall,
  "phone-off": PhoneOff,
  "phone-missed": PhoneMissed,
  supervise: Headphones,
};

// kind → chip classes, all from design tokens (theme-aware light/dark).
const kindChip: Record<EventKind, string> = {
  success: "bg-success-soft text-success",
  danger: "bg-danger-soft text-danger",
  accent: "bg-accent-soft text-accent",
  muted: "bg-surface-2 text-ink-faint",
};

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
function fmtStart(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const h = d.getHours() % 12 || 12;
  const ampm = d.getHours() >= 12 ? "pm" : "am";
  const m = d.getMinutes().toString().padStart(2, "0");
  const s = d.getSeconds().toString().padStart(2, "0");
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}, ${h}:${m}:${s} ${ampm}`;
}

// Donut geometry: r=40, circumference C; three arcs proportional to breakdown.
const R = 40;
const C = 2 * Math.PI * R;
const arcs = computed(() => {
  const b = props.call?.breakdown;
  if (!b) return [] as { dash: number; offset: number; color: string }[];
  const total = Math.max(1, b.talkSec + b.ringSec + b.ivrSec);
  const segs = [
    { len: b.talkSec, color: "var(--color-success)" },
    { len: b.ringSec, color: "var(--color-live)" },
    { len: b.ivrSec, color: "var(--color-border-strong)" },
  ];
  let cum = 0;
  return segs.map((sg) => {
    const dash = (sg.len / total) * C;
    const a = { dash, offset: -cum, color: sg.color };
    cum += dash;
    return a;
  });
});

const durationLabel = computed(() => `${Math.round(props.call?.durationSec ?? 0)}s`);
const dirIcon = computed(() => (props.call?.direction === "outbound" ? PhoneOutgoing : PhoneIncoming));

function close() {
  emit("update:open", false);
}
</script>

<template>
  <Modal :open="open" size="detail" @update:open="(v) => emit('update:open', v)">
    <div v-if="call">
      <!-- Header -->
      <div class="flex items-start gap-3">
        <component :is="dirIcon" class="mt-0.5 h-5 w-5 text-accent" />
        <div class="min-w-0 flex-1">
          <div class="mb-1 flex items-center gap-2">
            <Badge tone="success">Ended</Badge>
          </div>
          <div class="truncate text-base font-semibold text-ink">{{ call.caller }}</div>
          <div class="truncate text-sm text-ink-muted">→ {{ call.callee }}</div>
          <div class="mt-0.5 text-xs text-ink-faint">{{ fmtStart(call.startedAt) }}</div>
        </div>
        <button type="button" class="text-ink-faint hover:text-ink transition-colors" aria-label="Close" @click="close">
          <X class="h-4 w-4" />
        </button>
      </div>

      <!-- Duration ring + legend -->
      <div class="mt-4 flex items-center gap-5">
        <div class="relative h-24 w-24 shrink-0">
          <svg viewBox="0 0 100 100" class="h-24 w-24 -rotate-90">
            <circle cx="50" cy="50" :r="R" fill="none" stroke="var(--color-surface-2)" stroke-width="12" />
            <circle
              v-for="(a, i) in arcs"
              :key="i"
              cx="50"
              cy="50"
              :r="R"
              fill="none"
              :stroke="a.color"
              stroke-width="12"
              :stroke-dasharray="`${a.dash} ${C - a.dash}`"
              :stroke-dashoffset="a.offset"
            />
          </svg>
          <div class="absolute inset-0 flex flex-col items-center justify-center">
            <span class="text-lg font-semibold leading-none text-ink">{{ durationLabel }}</span>
            <span class="text-[10px] uppercase tracking-wide text-ink-faint">total</span>
          </div>
        </div>
        <div class="space-y-1.5 text-xs text-ink-muted">
          <div class="flex items-center gap-1.5">
            <span class="h-2 w-2 rounded-full" style="background: var(--color-success)"></span>
            talk {{ Math.round(call.breakdown.talkSec) }}s
          </div>
          <div class="flex items-center gap-1.5">
            <span class="h-2 w-2 rounded-full" style="background: var(--color-live)"></span>
            ring {{ Math.round(call.breakdown.ringSec) }}s
          </div>
          <div class="flex items-center gap-1.5">
            <span class="h-2 w-2 rounded-full" style="background: var(--color-border-strong)"></span>
            IVR {{ Math.round(call.breakdown.ivrSec) }}s
          </div>
        </div>
      </div>

      <!-- Stat strip -->
      <div class="-mx-5 mt-4 grid grid-cols-3 border-y border-border">
        <div class="border-r border-border px-2 py-3 text-center">
          <div class="text-lg font-semibold text-ink">{{ Math.round(call.stats.transfers) }}</div>
          <div class="text-[11px] text-ink-faint">transfers</div>
        </div>
        <div class="border-r border-border px-2 py-3 text-center">
          <div class="text-lg font-semibold text-ink">{{ Math.round(call.stats.agentsRung) }}</div>
          <div class="text-[11px] text-ink-faint">agents rung</div>
        </div>
        <div class="px-2 py-3 text-center">
          <div class="text-lg font-semibold text-ink">{{ Math.round(call.stats.missed) }}</div>
          <div class="text-[11px] text-ink-faint">missed (IVR)</div>
        </div>
      </div>

      <!-- Activity stream -->
      <div class="mt-4">
        <div class="mb-2 text-[11px] font-semibold uppercase tracking-widest text-ink-faint">Activity</div>
        <ul class="space-y-3">
          <li v-for="(e, i) in call.events" :key="i" class="flex items-start gap-3">
            <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full" :class="kindChip[e.kind]">
              <component :is="iconMap[e.icon]" class="h-3.5 w-3.5" />
            </span>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="text-sm text-ink">{{ e.label }}</span>
                <span
                  v-if="e.failed"
                  class="rounded px-1 text-[10px] font-medium bg-danger-soft text-danger"
                  >failed</span
                >
              </div>
              <div v-if="e.sub" class="text-xs text-ink-faint">{{ e.sub }}</div>
            </div>
            <span class="shrink-0 font-mono text-[11px] tabular-nums text-ink-faint">{{ e.time }}</span>
          </li>
        </ul>
      </div>

      <!-- Footer -->
      <div class="-mx-5 -mb-5 mt-4 border-t border-border px-5 py-3">
        <button
          type="button"
          class="flex w-full items-center justify-center gap-2 rounded-md bg-surface-2 py-2 text-sm font-medium text-ink hover:bg-surface-2/70 transition-colors"
          @click="emit('view-full-log', call.id)"
        >
          <ListTree class="h-4 w-4" />
          View full log
        </button>
      </div>
    </div>
    <div v-else class="py-8 text-center text-sm text-ink-faint">Loading…</div>
  </Modal>
</template>

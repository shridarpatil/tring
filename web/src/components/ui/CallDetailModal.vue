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
  Link2,
} from "lucide-vue-next";
import { copyText } from "../../lib/clipboard";
import { toast } from "../../lib/toast";
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

// The page URL is this call's shareable link (/calls/<id>).
async function copyLink() {
  if (await copyText(window.location.href)) toast.success("Link copied");
  else toast.error("Couldn't copy, copy the address bar instead");
}
</script>

<template>
  <Modal :open="open" size="detail" @update:open="(v) => emit('update:open', v)">
    <div v-if="call" class="cd">
      <!-- Header -->
      <div class="cd-head">
        <component :is="dirIcon" :size="20" class="cd-dir-icon" />
        <div class="cd-head-main">
          <div class="hstack gap-1 cd-status-row">
            <Badge tone="success">Ended</Badge>
          </div>
          <div class="truncate cd-caller">{{ call.caller }}</div>
          <div class="truncate text-light">→ {{ call.callee }}</div>
          <div class="text-lighter cd-started">{{ fmtStart(call.startedAt) }}</div>
        </div>
        <button type="button" class="ghost icon small" aria-label="Copy link" title="Copy link to this call" @click="copyLink">
          <Link2 :size="16" />
        </button>
        <button type="button" class="ghost icon small" aria-label="Close" @click="close">
          <X :size="16" />
        </button>
      </div>

      <!-- Duration ring + legend -->
      <div class="cd-duration">
        <div class="cd-ring">
          <svg viewBox="0 0 100 100" class="cd-ring-svg">
            <circle cx="50" cy="50" :r="R" fill="none" stroke="var(--secondary)" stroke-width="12" />
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
          <div class="cd-ring-label">
            <span class="cd-ring-total">{{ durationLabel }}</span>
            <span class="cd-ring-unit">total</span>
          </div>
        </div>
        <div class="cd-legend text-light">
          <div class="hstack gap-2">
            <span class="cd-legend-dot" style="background: var(--color-success)"></span>
            talk {{ Math.round(call.breakdown.talkSec) }}s
          </div>
          <div class="hstack gap-2">
            <span class="cd-legend-dot" style="background: var(--color-live)"></span>
            ring {{ Math.round(call.breakdown.ringSec) }}s
          </div>
          <div class="hstack gap-2">
            <span class="cd-legend-dot" style="background: var(--color-border-strong)"></span>
            IVR {{ Math.round(call.breakdown.ivrSec) }}s
          </div>
        </div>
      </div>

      <!-- Stat strip -->
      <div class="cd-stats">
        <div class="cd-stat">
          <div class="cd-stat-num">{{ Math.round(call.stats.transfers) }}</div>
          <div class="cd-stat-label text-lighter">transfers</div>
        </div>
        <div class="cd-stat">
          <div class="cd-stat-num">{{ Math.round(call.stats.agentsRung) }}</div>
          <div class="cd-stat-label text-lighter">agents rung</div>
        </div>
        <div class="cd-stat">
          <div class="cd-stat-num">{{ Math.round(call.stats.missed) }}</div>
          <div class="cd-stat-label text-lighter">missed (IVR)</div>
        </div>
      </div>

      <!-- Activity stream -->
      <div>
        <div class="cd-section-label text-lighter">Activity</div>
        <ul class="cd-activity">
          <li v-for="(e, i) in call.events" :key="i" class="cd-activity-row">
            <span class="cd-activity-icon" :data-kind="e.kind">
              <component :is="iconMap[e.icon]" :size="14" />
            </span>
            <div class="cd-activity-main">
              <div class="hstack gap-2">
                <span>{{ e.label }}</span>
                <Badge v-if="e.failed" tone="danger">failed</Badge>
              </div>
              <div v-if="e.sub" class="text-lighter cd-activity-sub">{{ e.sub }}</div>
            </div>
            <span class="mono cd-activity-time text-lighter">{{ e.time }}</span>
          </li>
        </ul>
      </div>

      <!-- Footer -->
      <div class="cd-footer">
        <button type="button" data-variant="secondary" class="w-100" @click="emit('view-full-log', call.id)">
          <ListTree :size="16" />
          View full log
        </button>
      </div>
    </div>
    <div v-else class="cd-loading text-lighter">Loading…</div>
  </Modal>
</template>

<style scoped>
/* Structural layout for the call summary body — Oat has no donut-chart,
   stat-strip, or activity-timeline primitive. Colours all via tokens. */
.cd-head { display: flex; align-items: flex-start; gap: 0.75rem; }
.cd-dir-icon { flex-shrink: 0; margin-top: 0.125rem; color: var(--primary); }
.cd-head-main { flex: 1 1 auto; min-width: 0; }
.cd-status-row { margin-bottom: 0.25rem; }
.cd-caller { font-size: var(--text-6); font-weight: 600; }
.cd-started { margin-top: 0.125rem; font-size: var(--text-8); }

.cd-duration { display: flex; align-items: center; gap: 1.25rem; margin-top: 1rem; }
.cd-ring { position: relative; width: 6rem; height: 6rem; flex-shrink: 0; }
.cd-ring-svg { width: 6rem; height: 6rem; transform: rotate(-90deg); }
.cd-ring-label {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}
.cd-ring-total { font-size: var(--text-5); font-weight: 600; line-height: 1; }
.cd-ring-unit { font-size: 10px; text-transform: uppercase; letter-spacing: 0.05em; color: var(--faint-foreground); }
.cd-legend { display: flex; flex-direction: column; gap: 0.375rem; font-size: var(--text-8); }
.cd-legend-dot { display: inline-block; width: 0.5rem; height: 0.5rem; border-radius: var(--radius-full); }

.cd-stats {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  margin: 1rem -1.25rem 0;
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
.cd-stat { padding: 0.75rem 0.5rem; text-align: center; border-right: 1px solid var(--border); }
.cd-stat:last-child { border-right: none; }
.cd-stat-num { font-size: var(--text-5); font-weight: 600; }
.cd-stat-label { font-size: 11px; }

.cd-section-label {
  margin-top: 1rem;
  margin-bottom: 0.5rem;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
}
.cd-activity { display: flex; flex-direction: column; gap: 0.75rem; list-style: none; margin: 0; padding: 0; }
.cd-activity-row { display: flex; align-items: flex-start; gap: 0.75rem; }
.cd-activity-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 1.75rem;
  height: 1.75rem;
  border-radius: var(--radius-full);
}
.cd-activity-icon[data-kind="success"] { color: var(--success); background: color-mix(in srgb, var(--success) 15%, transparent); }
.cd-activity-icon[data-kind="danger"] { color: var(--danger); background: color-mix(in srgb, var(--danger) 15%, transparent); }
.cd-activity-icon[data-kind="accent"] { color: var(--primary); background: color-mix(in srgb, var(--primary) 15%, transparent); }
.cd-activity-icon[data-kind="muted"] { color: var(--faint-foreground); background: var(--secondary); }
.cd-activity-main { flex: 1 1 auto; min-width: 0; }
.cd-activity-sub { font-size: var(--text-8); }
.cd-activity-time { flex-shrink: 0; font-size: 11px; font-variant-numeric: tabular-nums; }

.cd-footer { margin: 1rem -1.25rem -1.25rem; padding: 0.75rem 1.25rem; border-top: 1px solid var(--border); }
.cd-loading { padding: 2rem 0; text-align: center; font-size: var(--text-7); }
</style>

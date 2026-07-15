<script setup lang="ts">
import Modal from "./Modal.vue";
import { ArrowLeft, X } from "lucide-vue-next";
import type { FullLogRow } from "../../lib/callDetail";

defineProps<{ open: boolean; rows: FullLogRow[]; caller: string; callee: string }>();
const emit = defineEmits<{ (e: "update:open", v: boolean): void; (e: "back"): void }>();

// Source pill tone by lane. Both use design tokens.
function sourceCls(source: string): string {
  return source === "ivr" ? "bg-surface-2 text-ink-muted" : "bg-accent-soft text-accent";
}
</script>

<template>
  <Modal :open="open" size="wide" @update:open="(v) => emit('update:open', v)">
    <div class="space-y-3">
      <!-- Header -->
      <div class="flex items-center gap-3">
        <button
          type="button"
          class="flex items-center gap-1 text-sm text-ink-muted hover:text-ink transition-colors"
          @click="emit('back')"
        >
          <ArrowLeft class="h-4 w-4" />
          Back
        </button>
        <div class="min-w-0 flex-1 truncate text-sm font-medium text-ink">
          Full log — {{ caller }} → {{ callee }}
        </div>
        <button
          type="button"
          class="text-ink-faint hover:text-ink transition-colors"
          aria-label="Close"
          @click="emit('update:open', false)"
        >
          <X class="h-4 w-4" />
        </button>
      </div>

      <!-- Table -->
      <div class="-mx-5 overflow-auto max-h-[60vh] border-y border-border">
        <table class="w-full text-sm">
          <thead class="sticky top-0 z-10 bg-surface">
            <tr class="border-b border-border text-left">
              <th class="px-3 h-9 font-medium text-[11px] uppercase tracking-wide text-ink-faint">#</th>
              <th class="px-3 h-9 font-medium text-[11px] uppercase tracking-wide text-ink-faint">Timestamp</th>
              <th class="px-3 h-9 font-medium text-[11px] uppercase tracking-wide text-ink-faint">Offset</th>
              <th class="px-3 h-9 font-medium text-[11px] uppercase tracking-wide text-ink-faint">Source</th>
              <th class="px-3 h-9 font-medium text-[11px] uppercase tracking-wide text-ink-faint">Event</th>
              <th class="px-3 h-9 font-medium text-[11px] uppercase tracking-wide text-ink-faint">Actor</th>
              <th class="px-3 h-9 font-medium text-[11px] uppercase tracking-wide text-ink-faint">Detail</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.index" class="border-b border-border/60">
              <td class="px-3 py-2 text-ink-faint tabular-nums">{{ r.index }}</td>
              <td class="px-3 py-2 whitespace-nowrap font-mono text-xs text-ink-muted">{{ r.timestamp }}</td>
              <td class="px-3 py-2 whitespace-nowrap font-mono text-xs text-ink-faint">{{ r.offset }}</td>
              <td class="px-3 py-2">
                <span class="rounded px-1.5 py-0.5 text-[10px] font-medium" :class="sourceCls(r.source)">{{ r.source }}</span>
              </td>
              <td class="px-3 py-2 whitespace-nowrap font-mono text-xs text-ink">{{ r.event }}</td>
              <td class="px-3 py-2 whitespace-nowrap text-ink">{{ r.actor }}</td>
              <td class="px-3 py-2 text-ink-muted">{{ r.detail }}</td>
            </tr>
            <tr v-if="rows.length === 0">
              <td colspan="7" class="px-3 py-8 text-center text-ink-faint">No events recorded.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </Modal>
</template>

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
    <div class="cfl">
      <!-- Header -->
      <div class="cfl-head">
        <button type="button" class="ghost small" @click="emit('back')">
          <ArrowLeft :size="14" />
          Back
        </button>
        <div class="truncate cfl-title">Full log — {{ caller }} → {{ callee }}</div>
        <button type="button" class="ghost icon small" aria-label="Close" @click="emit('update:open', false)">
          <X :size="16" />
        </button>
      </div>

      <!-- Table -->
      <div class="table cfl-table-wrap">
        <table>
          <thead>
            <tr>
              <th class="align-right">#</th>
              <th>Timestamp</th>
              <th>Offset</th>
              <th>Source</th>
              <th>Event</th>
              <th>Actor</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.index">
              <td class="align-right mono text-lighter">{{ r.index }}</td>
              <td class="mono text-light" style="white-space: nowrap">{{ r.timestamp }}</td>
              <td class="mono text-lighter" style="white-space: nowrap">{{ r.offset }}</td>
              <td>
                <span class="badge" :data-variant="r.source === 'ivr' ? 'secondary' : 'accent'">{{ r.source }}</span>
              </td>
              <td class="mono" style="white-space: nowrap">{{ r.event }}</td>
              <td style="white-space: nowrap">{{ r.actor }}</td>
              <td class="text-light">{{ r.detail }}</td>
            </tr>
            <tr v-if="rows.length === 0">
              <td colspan="7" class="align-center text-lighter cfl-empty">No events recorded.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </Modal>
</template>

<style scoped>
/* Structural only — the sticky-header/max-height scroll behaviour has no
   Oat primitive (DataTable's maxHeight mode isn't usable here without
   touching <script>, since it isn't already imported). Colours via tokens. */
.cfl { display: flex; flex-direction: column; gap: 0.75rem; }
.cfl-head { display: flex; align-items: center; gap: 0.75rem; }
.cfl-title { flex: 1 1 auto; min-width: 0; font-size: var(--text-7); font-weight: 500; }
.cfl-table-wrap {
  max-height: 60vh;
  overflow: auto;
  margin: 0 -1.25rem;
  width: auto;
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
.cfl-table-wrap thead { position: sticky; top: 0; z-index: 10; background: var(--card); }
.cfl-empty { padding: 2.5rem 1rem; }
</style>

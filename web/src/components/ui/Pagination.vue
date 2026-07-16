<script setup lang="ts">
import { computed } from "vue";
import { ChevronLeft, ChevronRight } from "lucide-vue-next";

const props = withDefaults(
  defineProps<{
    page: number;
    pageSize: number;
    total: number;
    // Noun for the count label, e.g. "calls" -> "312 calls".
    label?: string;
  }>(),
  { label: "items" },
);

const emit = defineEmits<{ "update:page": [n: number] }>();

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)));

// Windowed page list: always show first + last, a window around the current
// page, and an ellipsis ("…") wherever there is a gap.
const pages = computed<(number | "…")[]>(() => {
  const last = totalPages.value;
  const cur = props.page;
  const window = new Set<number>([1, last, cur, cur - 1, cur + 1]);
  const nums = [...window].filter((n) => n >= 1 && n <= last).sort((a, b) => a - b);
  const out: (number | "…")[] = [];
  let prev = 0;
  for (const n of nums) {
    if (n - prev > 1) out.push("…");
    out.push(n);
    prev = n;
  }
  return out;
});

function go(n: number) {
  if (n < 1 || n > totalPages.value || n === props.page) return;
  emit("update:page", n);
}
</script>

<template>
  <div v-if="totalPages > 1" class="pager">
    <span class="pager-count">{{ total }} {{ label }}</span>

    <div class="hstack pager-controls">
      <button
        class="icon ghost small"
        :disabled="page <= 1"
        aria-label="Previous page"
        @click="go(page - 1)"
      >
        <ChevronLeft :size="16" />
      </button>

      <template v-for="(p, i) in pages" :key="i">
        <span v-if="p === '…'" class="pager-gap">…</span>
        <button v-else class="small" :class="p === page ? '' : 'ghost'" @click="go(p)">
          {{ p }}
        </button>
      </template>

      <button
        class="icon ghost small"
        :disabled="page >= totalPages"
        aria-label="Next page"
        @click="go(page + 1)"
      >
        <ChevronRight :size="16" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0 1rem;
  height: 3rem;
  border-top: 1px solid var(--border);
}
.pager-count { font-size: var(--text-7); color: var(--faint-foreground); }
.pager-controls { gap: 0.25rem; }
.pager-gap { padding: 0 0.375rem; color: var(--faint-foreground); user-select: none; }
</style>

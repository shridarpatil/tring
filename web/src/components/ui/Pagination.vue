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
  <div
    v-if="totalPages > 1"
    class="flex items-center justify-between gap-3 px-4 h-12 border-t border-border text-sm"
  >
    <span class="text-ink-faint">{{ total }} {{ label }}</span>

    <div class="flex items-center gap-1">
      <button
        class="inline-flex items-center justify-center h-8 w-8 rounded-md text-ink-muted hover:bg-surface-2/60 disabled:opacity-40 disabled:hover:bg-transparent"
        :disabled="page <= 1"
        aria-label="Previous page"
        @click="go(page - 1)"
      >
        <ChevronLeft class="h-4 w-4" />
      </button>

      <template v-for="(p, i) in pages" :key="i">
        <span v-if="p === '…'" class="px-1.5 text-ink-faint select-none">…</span>
        <button
          v-else
          class="inline-flex items-center justify-center h-8 min-w-8 px-2 rounded-md tabular-nums transition-colors"
          :class="
            p === page
              ? 'bg-accent text-accent-ink'
              : 'text-ink-muted hover:bg-surface-2/60'
          "
          @click="go(p)"
        >
          {{ p }}
        </button>
      </template>

      <button
        class="inline-flex items-center justify-center h-8 w-8 rounded-md text-ink-muted hover:bg-surface-2/60 disabled:opacity-40 disabled:hover:bg-transparent"
        :disabled="page >= totalPages"
        aria-label="Next page"
        @click="go(page + 1)"
      >
        <ChevronRight class="h-4 w-4" />
      </button>
    </div>
  </div>
</template>

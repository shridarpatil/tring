<script setup lang="ts" generic="T extends Record<string, any>">
import { Loader2 } from "lucide-vue-next";

export type Column = {
  key: string;
  label: string;
  mono?: boolean;
  align?: "left" | "right" | "center";
  width?: string;
};

withDefaults(
  defineProps<{
    columns: Column[];
    rows: T[];
    rowKey?: string;
    loading?: boolean;
    emptyText?: string;
  }>(),
  { rowKey: "id", emptyText: "Nothing here yet" },
);

defineSlots<{
  [key: `cell-${string}`]: (props: { row: T; value: unknown }) => unknown;
  "row-actions"?: (props: { row: T }) => unknown;
  empty?: () => unknown;
}>();

function alignCls(a?: string) {
  return a === "right" ? "text-right" : a === "center" ? "text-center" : "text-left";
}
</script>

<template>
  <div class="overflow-x-auto">
    <table class="w-full text-sm">
      <thead>
        <tr class="border-b border-border">
          <th
            v-for="c in columns"
            :key="c.key"
            class="px-4 h-10 font-medium text-xs uppercase tracking-wide text-ink-faint whitespace-nowrap"
            :class="alignCls(c.align)"
            :style="c.width ? { width: c.width } : {}"
          >
            {{ c.label }}
          </th>
          <th v-if="$slots['row-actions']" class="px-4 h-10 w-0"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="loading">
          <td
            :colspan="columns.length + ($slots['row-actions'] ? 1 : 0)"
            class="px-4 py-10 text-center text-ink-faint"
          >
            <Loader2 class="inline h-4 w-4 animate-spin mr-2" />Loading…
          </td>
        </tr>
        <tr v-else-if="rows.length === 0">
          <td :colspan="columns.length + ($slots['row-actions'] ? 1 : 0)" class="p-0">
            <slot name="empty">
              <div class="px-4 py-10 text-center text-ink-faint text-sm">{{ emptyText }}</div>
            </slot>
          </td>
        </tr>
        <tr
          v-for="row in rows"
          :key="row[rowKey]"
          class="border-b border-border/60 hover:bg-surface-2/40 transition-colors"
        >
          <td
            v-for="c in columns"
            :key="c.key"
            class="px-4 py-2.5 align-middle"
            :class="[
              alignCls(c.align),
              c.mono ? 'font-mono text-xs tabular-nums text-ink' : 'text-ink',
            ]"
          >
            <slot :name="`cell-${c.key}`" :row="row" :value="row[c.key]">{{ row[c.key] }}</slot>
          </td>
          <td v-if="$slots['row-actions']" class="px-4 py-2.5 text-right whitespace-nowrap">
            <slot name="row-actions" :row="row" />
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

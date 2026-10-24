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
    // When set (e.g. "calc(100vh-16rem)"), the body scrolls within this height
    // with a sticky header instead of growing the page. Omit for the default
    // (page-height, horizontal-scroll-only) behaviour.
    maxHeight?: string;
  }>(),
  { rowKey: "id", emptyText: "Nothing here yet" },
);

defineSlots<{
  [key: `cell-${string}`]: (props: { row: T; value: unknown }) => unknown;
  "row-actions"?: (props: { row: T }) => unknown;
  empty?: () => unknown;
}>();

const MONO = { fontFamily: "var(--font-mono)", fontVariantNumeric: "tabular-nums" as const };

function alignCls(a?: string) {
  return a === "right" ? "align-right" : a === "center" ? "align-center" : "align-left";
}
</script>

<template>
  <div class="table" :class="maxHeight ? 'dt-scroll' : ''" :style="maxHeight ? { maxHeight } : undefined">
    <table>
      <thead :class="maxHeight ? 'dt-sticky' : ''">
        <tr>
          <th
            v-for="c in columns"
            :key="c.key"
            :class="alignCls(c.align)"
            :style="c.width ? { width: c.width } : undefined"
          >
            {{ c.label }}
          </th>
          <th v-if="$slots['row-actions']"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="loading">
          <td :colspan="columns.length + ($slots['row-actions'] ? 1 : 0)" class="dt-msg">
            <Loader2 class="dt-spin" :size="16" />Loading…
          </td>
        </tr>
        <tr v-else-if="rows.length === 0">
          <td :colspan="columns.length + ($slots['row-actions'] ? 1 : 0)" style="padding: 0">
            <slot name="empty"><div class="dt-msg">{{ emptyText }}</div></slot>
          </td>
        </tr>
        <tr v-for="row in rows" :key="row[rowKey]">
          <td
            v-for="c in columns"
            :key="c.key"
            :class="alignCls(c.align)"
            :style="c.mono ? MONO : undefined"
          >
            <slot :name="`cell-${c.key}`" :row="row" :value="row[c.key]">{{ row[c.key] }}</slot>
          </td>
          <td v-if="$slots['row-actions']" class="align-right dt-actions">
            <slot name="row-actions" :row="row" />
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.dt-scroll { overflow: auto; }
.dt-sticky { position: sticky; top: 0; z-index: 10; background: var(--card); }
.dt-msg { padding: 2.5rem 1rem; text-align: center; color: var(--faint-foreground); }
.dt-actions { white-space: nowrap; }
/* A flex row of actions ignores the cell's text-align; push it right too. */
.dt-actions :slotted(.hstack) { justify-content: flex-end; flex-wrap: nowrap; }
.dt-spin { display: inline; vertical-align: -2px; margin-right: 0.5rem; animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
